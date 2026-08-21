// Package api wires together collector, detect, redact, ai, and store
// into actual HTTP handlers matching the endpoint table in Section 4 of
// the plan.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/store"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// Server holds every dependency the HTTP handlers need. Store, AIClient,
// and Hub are POINTERS THAT CAN BE NIL — per the plan's Section 12
// requirement, "works without LLM (detectors-only diagnosis stub) for
// graders" — so handlers must check for nil and degrade gracefully
// rather than assuming every dependency is always configured.
type Server struct {
	Collector           *collector.Collector
	Store               *store.Store // nil if DATABASE_URL not set
	AIClient            *ai.Client   // nil if AI_BASE_URL not set / no LLM key
	Hub                 *tunnel.Hub  // nil if remote-cluster relay is disabled
	TroubleshootTimeout time.Duration
	ClusterName         string
	ServerVersion       string
	// RelayPublicURL is the externally-reachable WebSocket URL agents
	// should dial (e.g. "wss://kubeassist.example.com/agent/connect")
	// — handed back verbatim in POST /api/clusters responses so the
	// frontend can render a working `RELAY_URL=...` value without
	// hardcoding it. Deliberately separate from whatever address the
	// HTTP server actually binds to (":8080"), since the two are almost
	// never the same string once anything sits in front of this
	// backend (a load balancer, TLS termination, a different public
	// hostname).
	RelayPublicURL string

	// LocalModeEnabled gates every route that exposes THIS server's own
	// configured cluster (Kubeconfig/KubeContext) — see
	// internal/config.Config.LocalModeEnabled for the full rationale.
	// When false, none of the local /api/pods, /api/namespaces,
	// /api/cluster, or local troubleshoot routes are even REGISTERED —
	// not just access-denied, genuinely absent from the router, so
	// there's no code path that could accidentally leak your cluster's
	// data to an unauthenticated caller.
	LocalModeEnabled bool
}

// NewRouter builds the *http.ServeMux with every route registered.
//
// WHY http.ServeMux INSTEAD OF A THIRD-PARTY ROUTER (chi, gin, etc.)?
// This is the decision we deferred back in Step 1. As of Go 1.22, the
// standard library's ServeMux gained two features that used to require
// a third-party router: (1) method-specific patterns ("GET /path" vs
// just "/path"), and (2) path WILDCARDS ("/api/pods/{namespace}/{name}"),
// retrievable via r.PathValue("namespace"). For a project of this size —
// a couple dozen routes, no need for route groups/middleware chaining
// beyond what we can compose by hand — the stdlib mux is now sufficient,
// and avoiding a dependency keeps the dependency tree smaller and the
// behavior fully specified by documentation we can actually read in
// full. We'd revisit this only if we needed things stdlib genuinely
// lacks: e.g. regex path segments, or a built-in middleware chaining
// convention.
func (s *Server) NewRouter() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)

	// Every route below touches THIS server's own configured cluster —
	// gated entirely behind LocalModeEnabled. A public multi-tenant
	// deployment sets ENABLE_LOCAL_MODE=false, and these routes simply
	// don't exist on the router at all: not a permission check that
	// could have a bug, an absent code path.
	if s.LocalModeEnabled {
		mux.HandleFunc("GET /api/cluster", s.handleCluster)
		mux.HandleFunc("GET /api/namespaces", s.handleNamespaces)
		mux.HandleFunc("GET /api/pods", s.handlePodList)
		mux.HandleFunc("GET /api/pods/{ns}/{name}", s.handlePodDetail)
		mux.HandleFunc("GET /api/pods/{ns}/{name}/logs", s.handlePodLogs)
		mux.HandleFunc("POST /api/pods/{ns}/{name}/troubleshoot", s.handleTroubleshoot)
		mux.HandleFunc("GET /api/pods/{ns}/{name}/troubleshoot/stream", s.handleTroubleshootStream)
	}

	// Investigation history isn't cluster-specific data leakage in the
	// same sense — it's whatever's already been saved, regardless of
	// which cluster (local or a customer's remote one) produced it —
	// so this stays available independent of LocalModeEnabled. NOTE:
	// this has its own real gap worth flagging: there's currently no
	// per-user scoping here either, so in a genuine multi-tenant
	// deployment ANY caller can list ANY customer's saved
	// investigations. Same class of problem as the /api/remote fix
	// below, not yet fixed for this specific endpoint.
	mux.HandleFunc("GET /api/investigations/{id}", s.handleGetInvestigation)
	mux.HandleFunc("GET /api/investigations", s.handleListInvestigations)

	// Remote-cluster relay routes — see internal/tunnel. The agent
	// connects here; user requests for a remote cluster's pods route
	// through handleRemoteTroubleshoot, which dispatches to whichever
	// agent is currently connected for that cluster ID.
	if s.Hub != nil {
		mux.HandleFunc("GET /agent/connect", s.Hub.ServeAgentConnect)
		mux.HandleFunc("POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot", s.handleRemoteTroubleshoot)
		mux.HandleFunc("GET /api/remote/{clusterId}/status", s.handleRemoteStatus)
		// Self-serve cluster registration — this is what powers a real
		// "Connect Your Cluster" onboarding flow in a frontend, as
		// opposed to an operator manually editing AGENT_TOKENS and
		// restarting the backend for every new customer.
		mux.HandleFunc("POST /api/clusters", s.handleRegisterCluster)
		mux.HandleFunc("GET /api/clusters", s.handleListClusters)
	}

	return mux
}

// --- Response helpers ---

// writeJSON is a small shared helper so every handler encodes responses
// identically (Content-Type header, status code, encoding errors).
// Centralizing this also means if we later want to add e.g. consistent
// response headers or logging, there's exactly one place to change.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// At this point headers are already sent, so we can't change
		// the status code — we can only log. This is a fundamental
		// limitation of HTTP: once you've started writing the response,
		// you can't take back what you already sent.
		fmt.Printf("api: failed encoding JSON response: %v\n", err)
	}
}

// errorEnvelope matches the plan's Section 4 requirement:
// error envelopes `{ code, message }`.
type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Code: code, Message: message})
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name":          s.ClusterName,
		"serverVersion": s.ServerVersion,
	})
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	namespaces, err := s.Collector.ListNamespaces(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "k8s_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, namespaces)
}

func (s *Server) handlePodList(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace") // optional filter, per API table's `?namespace=`
	pods, err := s.Collector.ListPods(r.Context(), namespace)
	if err != nil {
		writeError(w, http.StatusBadGateway, "k8s_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pods)
}

func (s *Server) handlePodDetail(w http.ResponseWriter, r *http.Request) {
	// r.PathValue extracts a named wildcard from the route pattern
	// ("/api/pods/{ns}/{name}") — this is the Go 1.22 ServeMux feature
	// mentioned above. Before 1.22, this required either manual string
	// splitting of r.URL.Path or a third-party router.
	ns := r.PathValue("ns")
	name := r.PathValue("name")

	snapshot, err := s.Collector.CollectCore(r.Context(), ns, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "pod_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	container := r.URL.Query().Get("container")
	previous := r.URL.Query().Get("previous") == "true"

	logs, err := s.Collector.FetchContainerLogs(r.Context(), ns, name, container, previous)
	if err != nil {
		writeError(w, http.StatusBadGateway, "log_fetch_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// handleTroubleshoot is the main orchestration endpoint for a LOCAL
// pod (in the cluster this backend is directly configured for): collect
// -> detect -> redact -> ask AI -> persist -> respond.
func (s *Server) handleTroubleshoot(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")

	// Parse the request body ONCE, up front — this used to happen
	// later, inside diagnoseAndRespond, but ServiceName now needs to be
	// known BEFORE the pass-2 collection decision below (it changes
	// which Service gets fetched). r.Body can only be read once; reading
	// it here and passing the parsed struct through avoids a real bug
	// that would otherwise appear the moment two things both tried to
	// decode it.
	var reqBody model.TroubleshootRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.TroubleshootTimeout)
	defer cancel()

	snapshot, err := s.Collector.CollectCore(ctx, ns, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "pod_not_found", err.Error())
		return
	}
	snapshot.ServiceName = reqBody.ServiceName

	// Local collection can do the pass-2 related-info fetch itself,
	// since it talks to the cluster directly — the remote/agent path
	// (handleRemoteTroubleshoot below) currently skips this, since it
	// would require a second round-trip through the tunnel. Worth
	// adding once the remote path is proven out.
	//
	// Pass-2 triggers (confirmed): ServiceSelectorMismatch suspected OR
	// an explicit serviceName was given -> Services+Endpoints;
	// CreateContainerConfigError suspected -> ConfigMap names/keys.
	// CollectRelated internally fetches whichever of these applies —
	// we just need to decide WHETHER to call it at all.
	core := detect.Detect(snapshot)
	snapshot.Detector = core
	if snapshotNeedsRelatedCheck(core, reqBody.ServiceName) {
		related, err := s.Collector.CollectRelated(ctx, ns, name, reqBody.ServiceName)
		if err == nil {
			snapshot.Related = related
			snapshot.Detector = detect.DetectRelated(snapshot, core)
		}
	}

	s.diagnoseAndRespond(w, ctx, ns, name, reqBody, snapshot)
}

// handleRemoteTroubleshoot is the equivalent endpoint for a pod living
// in a CUSTOMER'S cluster, reached via internal/tunnel instead of a
// local collector. The URL carries an extra {clusterId} segment
// identifying which connected agent to route the job to.
//
// Notice this function is almost entirely about ACQUIRING the snapshot
// differently — everything after that (detect, redact, ai, store) is
// identical to the local path, which is why we factored it into
// diagnoseAndRespond. This mirrors the architecture diagram: the tunnel
// only replaces HOW we reach the cluster, not what we do with what we
// find there.
//
// KNOWN GAP: this path does not yet run pass-2 CollectRelated (no
// remote ServiceSelectorMismatch/CreateContainerConfigError deep-dive)
// — the tunnel protocol would need a second JobRequest/JobResponse round
// trip to support that, which hasn't been built yet.
func (s *Server) handleRemoteTroubleshoot(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("clusterId")
	ns := r.PathValue("ns")
	name := r.PathValue("name")

	// AUTHORIZATION CHECK: this is the fix for a real gap — without
	// this, anyone who merely knew or guessed a clusterID string could
	// query THAT customer's cluster, regardless of who registered it.
	// See tunnel.Hub.VerifyToken's doc comment for the full reasoning.
	if !s.Hub.VerifyToken(clusterID, bearerToken(r)) {
		writeError(w, http.StatusUnauthorized, "invalid_cluster_token", "missing or incorrect Authorization token for this cluster")
		return
	}

	var reqBody model.TroubleshootRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.TroubleshootTimeout)
	defer cancel()

	snapshot, err := s.Hub.Dispatch(ctx, clusterID, tunnel.JobRequest{Namespace: ns, Pod: name})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent_unreachable", err.Error())
		return
	}
	snapshot.ServiceName = reqBody.ServiceName

	// Detection runs centrally here, not inside the agent — one place
	// owns classification logic for both local and remote pods, which
	// keeps internal/detect the single source of truth and makes future
	// changes to detection apply uniformly without redeploying every
	// customer's agent.
	snapshot.Detector = detect.Detect(snapshot)

	s.diagnoseAndRespond(w, ctx, ns, name, reqBody, snapshot)
}

// handleRemoteStatus lets the frontend show "cluster connected" /
// "cluster offline" before a user even tries to troubleshoot something.
// Also token-gated — connection status is minor information, but there's
// no reason to leak even that to an unauthenticated caller who doesn't
// hold this cluster's token.
func (s *Server) handleRemoteStatus(w http.ResponseWriter, r *http.Request) {
	clusterID := r.PathValue("clusterId")
	if !s.Hub.VerifyToken(clusterID, bearerToken(r)) {
		writeError(w, http.StatusUnauthorized, "invalid_cluster_token", "missing or incorrect Authorization token for this cluster")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connected": s.Hub.IsConnected(clusterID)})
}

// bearerToken extracts the token from a standard "Authorization: Bearer
// <token>" header, or returns "" if the header is absent or malformed
// — VerifyToken already treats an empty token as automatically invalid,
// so callers don't need to separately handle the "missing header" case.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimPrefix(h, prefix)
}

// registerClusterRequest is the body for POST /api/clusters.
type registerClusterRequest struct {
	ClusterID string `json:"clusterId"`
}

// registerClusterResponse is what the frontend needs to render a
// ready-to-copy agent deployment command/manifest — see the "download
// and connect" onboarding flow this powers.
type registerClusterResponse struct {
	ClusterID string `json:"clusterId"`
	Token     string `json:"token"`
	RelayURL  string `json:"relayUrl"`
}

// handleRegisterCluster is the actual backend support for a self-serve
// "Connect Your Cluster" UI flow: the frontend POSTs a user-chosen
// clusterID, and gets back a freshly generated token plus the relay
// WebSocket URL — everything needed to render a copy-pasteable
// `kubectl apply` manifest or Docker/Helm command for the user's agent.
//
// SECURITY NOTE: the returned token is shown exactly ONCE, in this
// response. We deliberately never expose it again via GET /api/clusters
// (which only returns connection status, not tokens) — this is the
// same "shown once at creation" pattern used by API key management UIs
// generally (GitHub personal access tokens, AWS access keys, etc.).
func (s *Server) handleRegisterCluster(w http.ResponseWriter, r *http.Request) {
	var req registerClusterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	if req.ClusterID == "" {
		writeError(w, http.StatusBadRequest, "missing_cluster_id", "clusterId is required")
		return
	}

	token, err := s.Hub.RegisterCluster(req.ClusterID)
	if err != nil {
		writeError(w, http.StatusConflict, "cluster_already_registered", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, registerClusterResponse{
		ClusterID: req.ClusterID,
		Token:     token,
		RelayURL:  s.RelayPublicURL,
	})
}

// handleListClusters powers a "your connected clusters" dashboard view
// — every registered cluster (pre-seeded or self-serve registered)
// along with its live connection status, but NEVER the tokens
// themselves (see the security note above).
func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Hub.ListClusters())
}

// diagnoseAndRespond is the shared tail end of both the local and remote
// troubleshoot flows: attach the user's question, redact, call the AI
// service EXACTLY ONCE (or fall back to a stub), persist, and respond.
// There is no multi-pass/recollect exchange with Python — every "gather
// more evidence" decision already happened in Go, before this function
// is ever called (see the pass-2 CollectRelated calls in the two
// handlers above). This function's whole job is straightforward:
// finalize the ALREADY-COMPLETE snapshot and make ONE request to Python.
func (s *Server) diagnoseAndRespond(w http.ResponseWriter, ctx context.Context, ns, name string, reqBody model.TroubleshootRequest, snapshot *model.DiagnosticSnapshot) {
	snapshot.UserQuestion = reqBody.Question
	redact.Snapshot(snapshot)

	if s.AIClient == nil {
		writeJSON(w, http.StatusOK, stubDiagnosis(snapshot.Detector))
		return
	}

	diagnosis, err := s.AIClient.Troubleshoot(ctx, snapshot)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ai_service_error", err.Error())
		return
	}

	if s.Store != nil {
		if _, err := s.Store.Save(ctx, ns, name, *snapshot, *diagnosis); err != nil {
			fmt.Printf("api: failed to save investigation: %v\n", err)
		}
	}

	writeJSON(w, http.StatusOK, diagnosis)
}

// handleTroubleshootStream implements the SSE pipeline-stage endpoint:
// collecting -> detecting -> retrieving -> diagnosing -> done, per
// Section 1 point 5 of the plan.
//
// WHY SERVER-SENT EVENTS (SSE) INSTEAD OF WEBSOCKETS?
// This is a ONE-DIRECTIONAL data flow: the server pushes stage updates,
// the client never needs to send anything back mid-stream. SSE is built
// directly on plain HTTP (a long-lived response the server writes to
// incrementally) — no separate protocol handshake, works through normal
// HTTP infrastructure (proxies, load balancers) without special
// configuration, and browsers have a native EventSource API for
// consuming it with automatic reconnection built in. WebSockets are a
// genuinely different protocol (upgraded from HTTP via a handshake) that
// earns its complexity when you need BIDIRECTIONAL, low-latency
// messaging (chat apps, multiplayer games). Since we only ever push
// progress updates one way, SSE is the simpler tool that fully covers
// the requirement — reaching for WebSockets here would be unjustified
// complexity.
func (s *Server) handleTroubleshootStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support flushing")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendStage := func(stage string) {
		// The SSE wire format is simple text: "data: <payload>\n\n" —
		// two trailing newlines mark the end of one event. We encode
		// our payload as JSON inside that data field so the frontend
		// gets structured data per stage, not just a bare string.
		payload, _ := json.Marshal(map[string]string{"stage": stage})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush() // without this, the write sits in a buffer and the client sees nothing until the buffer fills
	}

	ns := r.PathValue("ns")
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), s.TroubleshootTimeout)
	defer cancel()

	sendStage("collecting")
	snapshot, err := s.Collector.CollectCore(ctx, ns, name)
	if err != nil {
		sendStage("error")
		return
	}

	sendStage("detecting")
	snapshot.Detector = detect.Detect(snapshot)
	redact.Snapshot(snapshot)

	sendStage("retrieving")
	var diagnosis *model.DiagnosisResponse
	if s.AIClient != nil {
		diagnosis, err = s.AIClient.Troubleshoot(ctx, snapshot)
		if err != nil {
			sendStage("error")
			return
		}
	} else {
		d := stubDiagnosis(snapshot.Detector)
		diagnosis = &d
	}

	sendStage("diagnosing")

	payload, _ := json.Marshal(diagnosis)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", payload)
	flusher.Flush()
}

func (s *Server) handleGetInvestigation(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "investigation history is not configured (no DATABASE_URL)")
		return
	}
	id := r.PathValue("id")
	inv, err := s.Store.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "investigation_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) handleListInvestigations(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "store_unavailable", "investigation history is not configured (no DATABASE_URL)")
		return
	}
	namespace := r.URL.Query().Get("namespace")
	list, err := s.Store.List(r.Context(), namespace, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// snapshotNeedsRelatedCheck decides whether pass-2 collection is worth
// running, per the confirmed trigger table: a Service mismatch is
// plausible whenever the pod otherwise looks Healthy/Unknown (a
// container can be perfectly happy while its Service has zero
// endpoints) or when the caller explicitly named a serviceName to
// investigate; a ConfigMap problem is plausible whenever
// CreateContainerConfigError was already suspected from container
// state alone, and pass-2 fills in the specific key-level evidence.
func snapshotNeedsRelatedCheck(core model.DetectorResult, explicitServiceName *string) bool {
	if explicitServiceName != nil && *explicitServiceName != "" {
		return true
	}
	switch core.FailureType {
	case "", "Healthy", "Unknown", "CreateContainerConfigError":
		return true
	default:
		return false
	}
}

// stubDiagnosis builds the degraded-mode response when no LLM is
// configured (plan Section 12's required "works without LLM" path).
// Every field here is deliberately schema-COMPLIANT, not just
// plausible-looking: SchemaVersion is stamped, ResolutionSteps has at
// least one entry (the schema requires minItems: 1), and every required
// array field is initialized non-nil — a nil Go slice marshals to JSON
// `null`, which FAILS the schema's `"type": "array"` validation (unlike
// an empty `[]`, which passes). This was a real, easy-to-miss gap: Go's
// zero value for a slice and "a JSON array with zero elements" are NOT
// the same thing on the wire.
func stubDiagnosis(d model.DetectorResult) model.DiagnosisResponse {
	return model.DiagnosisResponse{
		SchemaVersion:     model.SchemaVersion(),
		FailureType:       d.FailureType,
		IssueSummary:      fmt.Sprintf("Detector classified this as: %s (AI diagnosis unavailable — no LLM configured)", d.FailureType),
		ObservedEvidence:  d.Evidence,
		ProbableRootCause: "AI service not configured — see detector evidence above",
		Reasoning:         "",
		ConfidenceScore:   d.Confidence,
		ResolutionSteps: []model.ResolutionStep{
			{Order: 1, Description: "Configure AI_BASE_URL and AI_AUTH_TOKEN for a full RAG-backed diagnosis with remediation steps."},
		},
		VerificationCommands:    []string{},
		RollbackGuidance:        "",
		DocumentationReferences: []model.DocumentationRef{},
		UncertaintyNotes:        []string{"Running in detector-only mode. Configure AI_BASE_URL and AI_AUTH_TOKEN for full RAG-backed diagnosis."},
		Mode:                    "detector_only_stub",
	}
}
