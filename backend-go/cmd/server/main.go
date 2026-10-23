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
	"strings"
	"syscall"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/api"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/auth"
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

	// Accounts gate every remote cluster. They're only needed with the
	// relay; local mode stays login-free (it's single-operator).
	var authMgr *auth.Manager
	if hub != nil {
		var accounts auth.Store = auth.NewMemoryStore()
		if st != nil {
			accounts = st
		} else {
			log.Println("warning: accounts are kept in memory without DATABASE_URL — users and sessions are lost on restart")
		}
		authCfg := auth.Config{
			PublicURL:      cfg.PublicURL,
			TrustedOrigins: cfg.CORSAllowedOrigins,
			SessionTTL:     cfg.SessionTTL,
			DevLogin:       cfg.AuthDevLogin,
		}
		if cfg.GitHubClientID != "" || cfg.GitHubClientSecret != "" {
			authCfg.GitHub = &auth.GitHubConfig{ClientID: cfg.GitHubClientID, ClientSecret: cfg.GitHubClientSecret}
		}
		authMgr, err = auth.NewManager(accounts, authCfg)
		if err != nil {
			log.Fatalf("auth: %v", err)
		}
		switch providers := authMgr.Providers(); {
		case len(providers) == 0:
			log.Println("warning: no sign-in method configured (set GITHUB_CLIENT_ID/GITHUB_CLIENT_SECRET) — nobody can register clusters; only ADMIN_TOKEN reaches existing ones")
		default:
			log.Printf("accounts enabled; sign-in via %v (UI at %s)", providers, cfg.PublicURL)
		}
		if cfg.AuthDevLogin {
			log.Println("WARNING: AUTH_DEV_LOGIN=true — anyone can sign in as any username. Never enable this on a reachable deployment.")
		}
	}

	// Alerts: a background watcher scans clusters whose owners enabled
	// alerts and posts new/resolved problems to their webhooks.
	var watcher *alerts.Watcher
	if hub != nil {
		var alertStore alerts.Store = alerts.NewMemoryStore()
		if st != nil {
			alertStore = st
		}
		if cfg.AlertsAllowPrivateWebhooks {
			log.Println("WARNING: ALERTS_ALLOW_PRIVATE_WEBHOOKS=true — alert webhooks may target http:// and internal addresses. Development only.")
		}
		publicURL := strings.TrimRight(cfg.PublicURL, "/")
		watcher = alerts.NewWatcher(alertStore, nil, alerts.NewSender(cfg.AlertsAllowPrivateWebhooks), cfg.AlertInterval,
			func(id string) string { return publicURL + "/c/" + id + "/scan" })
	}

	limiter := func(name, spec string) *api.RateLimiter {
		l, err := api.ParseRateLimit(spec)
		if err != nil {
			log.Fatalf("config error: %s: %v", name, err)
		}
		return l
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
		Auth:                authMgr,
		MaxClustersPerUser:  cfg.MaxClustersPerUser,
		RegisterLimiter:     limiter("RATE_LIMIT_REGISTER", cfg.RateLimitRegister),
		TroubleshootLimiter: limiter("RATE_LIMIT_TROUBLESHOOT", cfg.RateLimitTroubleshoot),
		LoginLimiter:        limiter("RATE_LIMIT_LOGIN", cfg.RateLimitLogin),
		TrustProxyHeaders:   cfg.TrustProxyHeaders,
		Alerts:              watcher,
		AlertInterval:       cfg.AlertInterval,
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

	if watcher != nil {
		watcher.SetScanner(server.AlertScanner())
		go watcher.Run(ctx)
		log.Printf("alerts: checking clusters with alerts enabled every %s", cfg.AlertInterval)
	}

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
