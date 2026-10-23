// Package api wires together collector, detect, redact, ai, store and
// tunnel into the HTTP API the frontend (and curl) talk to.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/auth"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/fixpr"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
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

	// AdminToken, when set, is an operator credential: as a Bearer token
	// it lists every registered cluster and can reach any of them.
	AdminToken string

	// Auth resolves browser sessions. Remote-cluster routes require a
	// signed-in user who owns the cluster (or AdminToken). nil means no
	// accounts — then only AdminToken can reach remote clusters.
	Auth *auth.Manager

	// MaxClustersPerUser caps registrations per account (0 = unlimited).
	MaxClustersPerUser int

	// Per-caller rate limits; nil = unlimited.
	RegisterLimiter     *RateLimiter // POST /api/clusters, per user
	TroubleshootLimiter *RateLimiter // troubleshoot routes, per user or IP
	LoginLimiter        *RateLimiter // sign-in endpoints, per IP

	// Alerts watches clusters and notifies their owners; nil disables
	// the /alerts routes.
	Alerts        *alerts.Watcher
	AlertInterval time.Duration

	// RepoLinks stores cluster -> GitHub repository links; with
	// repository access in Auth it enables fix pull requests.
	RepoLinks fixpr.LinkStore

	// TrustProxyHeaders makes rate limiting key anonymous callers by the
	// X-Forwarded-For address a trusted reverse proxy appended.
	TrustProxyHeaders bool
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
		troubleshoot := s.limitScoped(s.TroubleshootLimiter, s.handleTroubleshoot)
		stream := s.limitScoped(s.TroubleshootLimiter, s.handleTroubleshootStream)
		mux.HandleFunc("POST /api/pods/{ns}/{name}/troubleshoot", local(troubleshoot))
		mux.HandleFunc("GET /api/pods/{ns}/{name}/troubleshoot/stream", local(stream))
		mux.HandleFunc("POST /api/pods/{ns}/{name}/troubleshoot/stream", local(stream))
		mux.HandleFunc("POST /api/pods/{ns}/{name}/verify", local(s.limitScoped(s.TroubleshootLimiter, s.handleVerify)))
		mux.HandleFunc("POST /api/scan", local(s.limitScoped(s.TroubleshootLimiter, s.handleScan)))
		mux.HandleFunc("POST /api/chat", local(s.limitScoped(s.TroubleshootLimiter, s.handleChat)))
		// Local investigation history. Scoped to the local cluster only —
		// remote clusters' history is behind their owner's session below.
		mux.HandleFunc("GET /api/investigations", local(s.handleListInvestigations))
		mux.HandleFunc("GET /api/investigations/{id}", local(s.handleGetInvestigation))
	}

	if s.Auth != nil {
		s.Auth.RegisterRoutes(mux, s.limitByIP(s.LoginLimiter))
	}

	if s.Hub != nil {
		remote := s.remote
		// Agents authenticate with their per-cluster token; nothing else
		// does.
		mux.HandleFunc("GET /agent/connect", s.Hub.ServeAgentConnect)

		// Signed-in users register clusters and list their own.
		mux.HandleFunc("POST /api/clusters", s.handleRegisterCluster)
		mux.HandleFunc("GET /api/clusters", s.handleListClusters)
		mux.HandleFunc("DELETE /api/clusters/{clusterId}", remote(s.handleRevokeCluster))

		mux.HandleFunc("GET /api/remote/{clusterId}/status", remote(s.handleRemoteStatus))
		mux.HandleFunc("GET /api/remote/{clusterId}/namespaces", remote(s.handleNamespaces))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods", remote(s.handlePodList))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods/{ns}/{name}", remote(s.handlePodDetail))
		mux.HandleFunc("GET /api/remote/{clusterId}/pods/{ns}/{name}/logs", remote(s.handlePodLogs))
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot", remote(s.limitScoped(s.TroubleshootLimiter, s.handleTroubleshoot)))
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot/stream", remote(s.limitScoped(s.TroubleshootLimiter, s.handleTroubleshootStream)))
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/verify", remote(s.limitScoped(s.TroubleshootLimiter, s.handleVerify)))
		mux.HandleFunc("POST /api/remote/{clusterId}/scan", remote(s.limitScoped(s.TroubleshootLimiter, s.handleScan)))
		mux.HandleFunc("POST /api/remote/{clusterId}/chat", remote(s.limitScoped(s.TroubleshootLimiter, s.handleChat)))
		if s.fixPRsEnabled() {
			mux.HandleFunc("GET /api/remote/{clusterId}/repo", remote(s.handleGetRepoLink))
			mux.HandleFunc("PUT /api/remote/{clusterId}/repo", remote(s.handlePutRepoLink))
			mux.HandleFunc("DELETE /api/remote/{clusterId}/repo", remote(s.handleDeleteRepoLink))
			mux.HandleFunc("GET /api/remote/{clusterId}/pods/{ns}/{name}/fix-pr", remote(s.handleFixOptions))
			mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/fix-pr", remote(s.limitScoped(s.TroubleshootLimiter, s.handleOpenFixPR)))
		}
		if s.Alerts != nil {
			mux.HandleFunc("GET /api/remote/{clusterId}/alerts", remote(s.handleGetAlerts))
			mux.HandleFunc("PUT /api/remote/{clusterId}/alerts", remote(s.handlePutAlerts))
			mux.HandleFunc("DELETE /api/remote/{clusterId}/alerts", remote(s.handleDeleteAlerts))
			mux.HandleFunc("POST /api/remote/{clusterId}/alerts/test", remote(s.limitScoped(s.TroubleshootLimiter, s.handleTestAlert)))
		}
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

// remote resolves {clusterId} and authorizes EVERY request: the caller
// must be signed in as the cluster's owner (or present AdminToken).
// A cluster that doesn't exist and one owned by someone else get the
// same 404, so cluster IDs can't be enumerated.
func (s *Server) remote(h scopedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clusterID := r.PathValue("clusterId")
		if !s.isAdmin(r) {
			u, ok := s.requireUser(w, r)
			if !ok {
				return
			}
			if owner, exists := s.Hub.Owner(clusterID); !exists || owner == "" || owner != u.ID {
				writeError(w, http.StatusNotFound, "cluster_not_found", "no cluster with that ID in your account")
				return
			}
			r = r.WithContext(withUser(r.Context(), u))
		}
		h(w, r, remoteSource{hub: s.Hub, clusterID: clusterID}, clusterID)
	}
}

// requireUser resolves the session or writes 401/403/503 and returns false.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (*model.User, bool) {
	if s.Auth == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "accounts are not enabled on this server")
		return nil, false
	}
	u, err := s.Auth.CurrentUser(r)
	switch {
	case errors.Is(err, auth.ErrUntrustedOrigin):
		writeError(w, http.StatusForbidden, "untrusted_origin", err.Error())
		return nil, false
	case err != nil:
		log.Printf("api: resolving session: %v", err)
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "could not check your session")
		return nil, false
	case u == nil:
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to use remote clusters")
		return nil, false
	}
	return u, true
}

// isAdmin reports whether the request carries AdminToken.
func (s *Server) isAdmin(r *http.Request) bool {
	return s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(bearerToken(r)), []byte(s.AdminToken)) == 1
}

type userKey struct{}

func withUser(ctx context.Context, u *model.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

func userFrom(ctx context.Context) *model.User {
	u, _ := ctx.Value(userKey{}).(*model.User)
	return u
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
	// AuthProviders lists the sign-in methods ("github", "dev"); empty
	// when accounts are disabled.
	AuthProviders []string `json:"authProviders"`
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

		AuthProviders: []string{},
	}
	if s.Auth != nil {
		resp.AuthProviders = s.Auth.Providers()
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
	if s.Auth != nil {
		resp.Features = append(resp.Features, "accounts")
	}
	if s.fixPRsEnabled() {
		resp.Features = append(resp.Features, "fix-prs")
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name":          s.ClusterName,
		"serverVersion": s.ServerVersion,
	})
}
