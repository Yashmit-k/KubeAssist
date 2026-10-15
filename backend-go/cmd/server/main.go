// Package main is the entrypoint for the KubeAssist Go backend. Its only
// job is construction and wiring: read config, build each dependency in
// the right order, inject them into the API server, and start listening.
// No business logic lives here.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/api"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/config"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/k8s"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/store"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// The local cluster is only needed in local mode. A public
	// multi-tenant deployment (ENABLE_LOCAL_MODE=false) runs with no
	// kubeconfig or in-cluster credentials at all.
	var (
		coll          *collector.Collector
		clusterName   = cfg.KubeContext
		serverVersion string
	)
	if cfg.LocalModeEnabled {
		clientset, err := k8s.NewClientset(cfg.Kubeconfig, cfg.KubeContext)
		if err != nil {
			log.Fatalf("k8s client error (set ENABLE_LOCAL_MODE=false to run without a local cluster): %v", err)
		}
		if v, err := clientset.Discovery().ServerVersion(); err == nil {
			serverVersion = v.GitVersion
		} else {
			log.Printf("warning: could not fetch cluster server version: %v", err)
		}
		if clusterName == "" {
			clusterName = "local"
		}
		coll = collector.New(clientset, clusterName, serverVersion)
		log.Printf("LOCAL MODE ENABLED — cluster %q (%s) is reachable via the API. Set ENABLE_LOCAL_MODE=false for a public multi-tenant deployment.", clusterName, serverVersion)
	} else {
		log.Println("local mode disabled — only registered remote clusters are reachable via /api/remote/...")
	}

	// Store and AI client are OPTIONAL; the API degrades without them.
	var st *store.Store
	if cfg.DatabaseURL != "" {
		st, err = store.New(cfg.DatabaseURL, cfg.DBConnectTimeout)
		if err != nil {
			log.Fatalf("store error: %v", err)
		}
		defer st.Close()

		migrateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = st.Migrate(migrateCtx)
		cancel()
		if err != nil {
			log.Fatalf("store migration error: %v", err)
		}
		log.Println("investigation history enabled (Postgres)")
	} else {
		log.Println("DATABASE_URL not set — investigation history disabled")
	}

	var aiClient *ai.Client
	if cfg.AIBaseURL != "" && cfg.AIAuthToken != "" {
		aiClient = ai.New(cfg.AIBaseURL, cfg.AIAuthToken, cfg.AITimeout)
		log.Printf("AI service: %s", cfg.AIBaseURL)
	} else {
		log.Println("AI_BASE_URL/AI_AUTH_TOKEN not fully configured — running in detector-only mode")
	}

	// The relay hub is built whenever ENABLE_RELAY=true, even with zero
	// pre-seeded clusters, so the first self-serve registration works.
	var hub *tunnel.Hub
	if cfg.RelayEnabled {
		hub = tunnel.NewHub(cfg.AgentTokens)
		if st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := hub.AttachStore(ctx, st)
			cancel()
			if err != nil {
				log.Fatalf("relay: %v", err)
			}
			log.Println("remote-cluster registrations are persisted to Postgres")
		} else {
			log.Println("warning: relay enabled without DATABASE_URL — self-serve cluster registrations are lost on restart")
		}
		log.Printf("remote-cluster relay enabled (%d registered cluster(s)); agents dial %s", len(hub.ListClusters()), cfg.RelayPublicURL)
	} else {
		log.Println("ENABLE_RELAY not set — remote-cluster relay disabled")
	}

	if !cfg.LocalModeEnabled && hub == nil {
		log.Println("warning: both local mode and the relay are disabled — the API can only serve /api/health")
	}

	server := &api.Server{
		Collector:           coll,
		Store:               st,
		AIClient:            aiClient,
		Hub:                 hub,
		TroubleshootTimeout: cfg.TroubleshootTimeout,
		ClusterName:         clusterName,
		ServerVersion:       serverVersion,
		RelayPublicURL:      cfg.RelayPublicURL,
		AgentImage:          os.Getenv("AGENT_IMAGE"),
		LocalModeEnabled:    cfg.LocalModeEnabled,
		AdminToken:          cfg.AdminToken,
	}

	handler := api.NewCORSMiddleware(cfg.CORSAllowedOrigins)(api.RequestLogger(server.NewRouter()))

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE streams and the agent WebSocket are
		// long-lived; per-request work is bounded by context timeouts.
		IdleTimeout: 120 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM (what Kubernetes sends before
	// killing a pod): stop accepting connections, let in-flight requests
	// finish within a bounded window.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("KubeAssist backend %s listening on %s", api.Version, httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received, draining in-flight requests...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}
