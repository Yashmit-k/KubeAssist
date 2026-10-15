// Package main is the entrypoint for the KubeAssist Go backend. Its ONLY
// job is construction and wiring: read config, build each dependency in
// the right order, inject them into the API server, and start listening.
// No business logic lives here — that discipline is what keeps every
// other package independently testable.
package main

import (
	"context"
	"log"
	"net/http"
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

	clientset, err := k8s.NewClientset(cfg.Kubeconfig, cfg.KubeContext)
	if err != nil {
		log.Fatalf("k8s client error: %v", err)
	}

	serverVersion := ""
	if v, err := clientset.Discovery().ServerVersion(); err == nil {
		serverVersion = v.GitVersion
	} else {
		log.Printf("warning: could not fetch cluster server version: %v", err)
	}

	coll := collector.New(clientset, cfg.KubeContext, serverVersion)

	// Store and AI client are OPTIONAL — this is the degraded-mode
	// behavior required by the plan (Section 12: "works without LLM").
	// We only construct them if their required config is present, and
	// the Server struct in internal/api is written to handle a nil
	// pointer for either one.
	var st *store.Store
	if cfg.DatabaseURL != "" {
		st, err = store.New(cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("store error: %v", err)
		}
		defer st.Close()

		migrateCtx, cancel := context.WithTimeout(context.Background(), cfg.K8sTimeout)
		if err := st.Migrate(migrateCtx); err != nil {
			cancel()
			log.Fatalf("store migration error: %v", err)
		}
		cancel()
	} else {
		log.Println("DATABASE_URL not set — investigation history disabled")
	}

	var aiClient *ai.Client
	if cfg.AIBaseURL != "" && cfg.AIAuthToken != "" {
		aiClient = ai.New(cfg.AIBaseURL, cfg.AIAuthToken, cfg.AITimeout)
	} else {
		log.Println("AI_BASE_URL/AI_AUTH_TOKEN not fully configured — running in detector-only mode")
	}

	// The relay hub is built whenever ENABLE_RELAY=true — deliberately
	// NOT gated on AgentTokens being non-empty anymore. A fresh
	// deployment with zero pre-configured clusters still needs a live
	// Hub to accept its FIRST self-serve registration via
	// POST /api/clusters; gating construction on "already has tokens"
	// would make that impossible.
	var hub *tunnel.Hub
	if cfg.RelayEnabled {
		hub = tunnel.NewHub(cfg.AgentTokens)
		log.Printf("remote-cluster relay enabled (%d pre-seeded cluster(s), self-serve registration open)", len(cfg.AgentTokens))
	} else {
		log.Println("ENABLE_RELAY not set — remote-cluster relay disabled")
	}

	server := &api.Server{
		Collector:           coll,
		Store:               st,
		AIClient:            aiClient,
		Hub:                 hub,
		TroubleshootTimeout: cfg.TroubleshootTimeout,
		ClusterName:         cfg.KubeContext,
		ServerVersion:       serverVersion,
		RelayPublicURL:      cfg.RelayPublicURL,
		LocalModeEnabled:    cfg.LocalModeEnabled,
	}

	if cfg.LocalModeEnabled {
		log.Println("LOCAL MODE ENABLED — this server's own configured cluster is reachable via the public API. Set ENABLE_LOCAL_MODE=false for a public multi-tenant deployment.")
	} else {
		log.Println("local mode disabled — this server's own cluster is NOT exposed; only registered remote clusters via /api/remote/... are reachable")
	}

	// Wrap the router with CORS support so a browser-based frontend on a
	// different origin (Vite dev server, deployed React app, etc.) can
	// call this API. server.NewRouter() itself is completely unchanged
	// — this just layers a middleware around its output, per
	// internal/api/cors.go.
	corsHandler := api.NewCORSMiddleware(cfg.CORSAllowedOrigins)(server.NewRouter())

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: corsHandler,
	}

	// GRACEFUL SHUTDOWN: signal.NotifyContext gives us a context that's
	// automatically cancelled when the process receives SIGINT (Ctrl+C)
	// or SIGTERM (what Kubernetes sends a Pod before killing it during a
	// rolling update or scale-down). Handling this properly matters a
	// lot once this runs IN a cluster: without it, Kubernetes sends
	// SIGTERM, waits out a grace period (default 30s), then SIGKILLs the
	// process — any in-flight request gets dropped mid-response. With
	// graceful shutdown, we stop accepting NEW connections immediately
	// but let IN-FLIGHT requests finish (up to a bounded timeout).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("KubeAssist backend listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done() // blocks here until SIGINT/SIGTERM
	log.Println("shutdown signal received, draining in-flight requests...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	log.Println("shutdown complete")
}
