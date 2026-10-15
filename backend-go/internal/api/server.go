// Package api wires together collector, detect, redact, ai, store and
// tunnel into the HTTP API the frontend (and curl) talk to.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/store"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// Version is stamped at build time via -ldflags "-X .../api.Version=...".
var Version = "dev"

// Server holds every dependency the HTTP handlers need. Collector,
// Store, AIClient and Hub are POINTERS THAT CAN BE NIL — the backend
// degrades gracefully: no DB means no history, no AI means
// detector-only diagnoses, no Hub means no remote clusters, and no
// Collector means local mode is off.
type Server struct {
	Collector           *collector.Collector // nil if local mode is disabled
	Store               *store.Store         // nil if DATABASE_URL not set
	AIClient            *ai.Client           // nil if AI_BASE_URL/AI_AUTH_TOKEN not set
	Hub                 *tunnel.Hub          // nil if the relay is disabled
	TroubleshootTimeout time.Duration
	ClusterName         string
	ServerVersion       string

	// RelayPublicURL is the externally-reachable WebSocket URL agents
	// should dial — returned verbatim from POST /api/clusters so the UI
	// can render a working agent manifest. It differs from the bind
	// address once a load balancer / TLS terminator sits in front.
	RelayPublicURL string

	// AgentImage is the container image the generated agent manifest uses.
	AgentImage string

	// LocalModeEnabled gates every route that exposes THIS server's own
	// cluster. When false those routes are not registered at all — an
	// absent code path, not a permission check that could have a bug.
	LocalModeEnabled bool

	// AdminToken, when set, authorizes listing all registered clusters.
	AdminToken string
}

// NewRouter builds the *http.ServeMux with every route registered. Go
// 1.22+ ServeMux supports method patterns and {wildcards}, which is all
// this API needs — no third-party router.
func (s *Server) NewRouter() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)

	if s.LocalModeEnabled && s.Collector != nil {
		local := s.local
		mux.HandleFunc("GET /api/cluster", s.handleCluster)
		mux.HandleFunc("GET /api/namespaces", local(s.handleNamespaces))
		mux.HandleFunc("GET /api/pods", local(s.handlePodList))
		mux.HandleFunc("GET /api/pods/{ns}/{name}", local(s.handlePodDetail))
		mux.HandleFunc("GET /api/pods/{ns}/{name}/logs", local(s.handlePodLogs))
		mux.HandleFunc("POST /api/pods/{ns}/{name}/troubleshoot", local(s.handleTroubleshoot))
		mux.HandleFunc("GET /api/pods/{ns}/{name}/troubleshoot/stream", local(s.handleTroubleshootStream))
		mux.HandleFunc("POST /api/pods/{ns}/{name}/troubleshoot/stream", local(s.handleTroubleshootStream))
		// Local investigation history. Scoped to the local cluster only —
		// remote clusters' history is behind their own token below.
		mux.HandleFunc("GET /api/investigations", local(s.handleListInvestigations))
		mux.HandleFunc("GET /api/investigations/{id}", local(s.handleGetInvestigation))
	}

	if s.Hub != nil {
		remote := s.remote
		mux.HandleFunc("GET /agent/connect", s.Hub.ServeAgentConnect)

		// Self-serve registration powering the "Connect your cluster" page.
		mux.HandleFunc("POST /api/clusters", s.handleRegisterCluster)
		mux.HandleFunc("GET /api/clusters", s.handleListClusters)
		mux.HandleFunc("DELETE /api/clusters/{clusterId}", remote(s.handleRevokeCluster))

		mux.HandleFunc("GET /api/remote/{clusterId}/status", remote(s.handleRemoteStatus))
		mux.HandleFunc("GET /api/remote/{clusterId}/namespaces", remote(s.handleNamespaces))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods", remote(s.handlePodList))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods/{ns}/{name}", remote(s.handlePodDetail))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods/{ns}/{name}/logs", remote(s.handlePodLogs))
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot", remote(s.handleTroubleshoot))
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot/stream", remote(s.handleTroubleshootStream))
		mux.HandleFunc("GET /api/remote/{clusterId}/investigations", remote(s.handleListInvestigations))
		mux.HandleFunc("GET /api/remote/{clusterId}/investigations/{id}", remote(s.handleGetInvestigation))
	}

	return mux
}

// scopedHandler is a handler that has already been resolved to a
// cluster: src talks to it and clusterKey scopes its stored history.
type scopedHandler func(w http.ResponseWriter, r *http.Request, src clusterSource, clusterKey string)

func (s *Server) local(h scopedHandler) http.HandlerFunc {
	src := redactingSource{s.Collector}
	return func(w http.ResponseWriter, r *http.Request) {
		h(w, r, src, store.LocalCluster)
	}
}

// remote resolves {clusterId} and enforces its bearer token on EVERY
// request — knowing or guessing a cluster ID is not enough to read it.
func (s *Server) remote(h scopedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clusterID := r.PathValue("clusterId")
		if !s.Hub.VerifyToken(clusterID, bearerToken(r)) {
			writeError(w, http.StatusUnauthorized, "invalid_cluster_token", "missing or incorrect Authorization token for this cluster")
			return
		}
		h(w, r, remoteSource{hub: s.Hub, clusterID: clusterID}, clusterID)
	}
}

// --- Response helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; all we can do is log.
		log.Printf("api: failed encoding JSON response: %v", err)
	}
}

// errorEnvelope is the uniform error body: { code, message }.
type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Code: code, Message: message})
}

// writeSourceError maps a cluster-source error onto an HTTP status.
func writeSourceError(w http.ResponseWriter, err error, notFoundCode string) {
	switch {
	case isNotFound(err):
		writeError(w, http.StatusNotFound, notFoundCode, err.Error())
	case errors.Is(err, tunnel.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, "agent_not_connected", "the agent for this cluster is not connected")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", err.Error())
	default:
		writeError(w, http.StatusBadGateway, "cluster_error", err.Error())
	}
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// --- Health ---

type healthResponse struct {
	Status    string   `json:"status"`
	Version   string   `json:"version"`
	LocalMode bool     `json:"localMode"`
	Relay     bool     `json:"relay"`
	AI        string   `json:"ai"`    // ok | unavailable | disabled
	Store     string   `json:"store"` // ok | unavailable | disabled
	Features  []string `json:"features"`
}

// handleHealth reports liveness plus which optional components are
// configured and reachable. It always returns 200 while the process is
// up (so it is safe as a liveness probe); degraded components show up in
// the body, which the UI uses to decide what to offer.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	resp := healthResponse{
		Status:    "ok",
		Version:   Version,
		LocalMode: s.LocalModeEnabled && s.Collector != nil,
		Relay:     s.Hub != nil,
		AI:        "disabled",
		Store:     "disabled",
		Features:  []string{},
	}
	if s.AIClient != nil {
		resp.AI = "ok"
		if err := s.AIClient.Health(ctx); err != nil {
			resp.AI = "unavailable"
		}
	}
	if s.Store != nil {
		resp.Store = "ok"
		if err := s.Store.Ping(ctx); err != nil {
			resp.Store = "unavailable"
		}
	}
	if resp.LocalMode {
		resp.Features = append(resp.Features, "local")
	}
	if resp.Relay {
		resp.Features = append(resp.Features, "remote")
	}
	if s.Store != nil {
		resp.Features = append(resp.Features, "history")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name":          s.ClusterName,
		"serverVersion": s.ServerVersion,
	})
}
