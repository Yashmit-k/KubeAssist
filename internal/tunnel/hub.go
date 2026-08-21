package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/yourname/kubeassist/backend-go/internal/model"
)

// Hub tracks every currently-connected agent (one per customer cluster)
// and lets the rest of the backend dispatch jobs to a specific one by
// cluster ID.
//
// WHY A CENTRAL Hub STRUCT RATHER THAN LETTING EACH CONNECTION HANDLE
// ITSELF INDEPENDENTLY?
// A troubleshoot HTTP request arrives on a completely different
// goroutine than the one reading the agent's WebSocket connection —
// they need a shared place to hand off "here's a job" and "here's the
// matching result." Hub is exactly that shared coordination point,
// protected by a mutex since multiple HTTP requests can dispatch to
// (or agents can register with) the Hub concurrently.
type Hub struct {
	mu     sync.Mutex
	agents map[string]*agentConn // keyed by ClusterID

	// tokens maps ClusterID -> shared secret token. Pre-seeded from
	// AGENT_TOKENS at startup, and mutable at runtime via
	// RegisterCluster (the dynamic self-serve registration path). A
	// real multi-tenant product would persist this in Postgres with
	// rotation/revocation support and survive a restart; this in-memory
	// map is the right complexity for proving the self-serve flow works
	// end to end — restarting the backend currently forgets dynamically
	// registered clusters, a known, documented limitation.
	tokens map[string]string

	upgrader websocket.Upgrader
}

// agentConn wraps one agent's live WebSocket connection plus the
// bookkeeping needed to route JobResponses back to whichever goroutine
// is waiting for them.
type agentConn struct {
	clusterID string
	conn      *websocket.Conn
	writeMu   sync.Mutex // websocket connections are not safe for concurrent writes — see below

	pendingMu sync.Mutex
	pending   map[string]chan JobResponse // keyed by JobID
}

// NewHub constructs a Hub from a static clusterID->token map (parsed
// from the AGENT_TOKENS env var — see config.go).
// NewHub constructs a Hub from a static clusterID->token map (parsed
// from the AGENT_TOKENS env var — see config.go), or nil/empty if no
// clusters are pre-seeded and every cluster will register dynamically
// via RegisterCluster instead.
func NewHub(tokens map[string]string) *Hub {
	// A nil map is a real possibility here — not just from a caller
	// being careless, but from Go's own zero-value semantics: a
	// map[string]string variable that was never explicitly initialized
	// IS nil, and it's easy to end up passing one in (e.g. a struct
	// field that was never set). Reading from a nil map is safe in Go
	// (returns the zero value), but WRITING to one panics — and
	// RegisterCluster writes. So NewHub must normalize nil to an empty,
	// writable map right here, rather than trusting every caller to
	// remember to do so. This was a real bug: a test calling
	// NewHub(nil) — a completely reasonable thing to do, simulating "no
	// pre-seeded tokens" — panicked on the first dynamic registration.
	if tokens == nil {
		tokens = make(map[string]string)
	}

	return &Hub{
		agents: make(map[string]*agentConn),
		tokens: tokens,
		upgrader: websocket.Upgrader{
			// In production, CheckOrigin should validate the connecting
			// origin properly. Agents aren't browsers, so the default
			// same-origin browser check doesn't apply the same way —
			// we allow all origins here and rely on the TOKEN for
			// authentication instead, which is the actual security
			// boundary for a non-browser client.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// ServeAgentConnect handles GET /agent/connect — an agent authenticates
// via query params, then this connection stays open for the agent's
// entire lifetime.
//
// WHY QUERY PARAMS FOR AUTH INSTEAD OF A REQUEST BODY OR HEADER?
// The WebSocket handshake is itself a plain HTTP GET request (that's
// literally the "Upgrade" mechanism) — it has no body, and while
// headers ARE possible, query params are simpler for a small Go agent
// binary that doesn't need a full HTTP client library just to set a
// custom header on the upgrade request. For anything more sensitive
// than a bearer token, a header (Authorization) would be preferable
// since query params can end up in access logs — worth revisiting
// before this handles anything more sensitive than what we already
// scope via read-only RBAC.
func (h *Hub) ServeAgentConnect(w http.ResponseWriter, r *http.Request) {
	clusterID := r.URL.Query().Get("clusterId")
	token := r.URL.Query().Get("token")

	// tokens can now be mutated at runtime by RegisterCluster (dynamic
	// self-serve registration), unlike the original static-only design
	// — so this read must be lock-protected too, reusing the same mu
	// that already guards `agents`, rather than adding a second mutex
	// for what's genuinely the same "Hub's mutable state" concern.
	h.mu.Lock()
	expected, ok := h.tokens[clusterID]
	h.mu.Unlock()

	if !ok || expected == "" || expected != token {
		http.Error(w, "invalid cluster id or token", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("tunnel: websocket upgrade failed for cluster %q: %v", clusterID, err)
		return
	}

	ac := &agentConn{
		clusterID: clusterID,
		conn:      conn,
		pending:   make(map[string]chan JobResponse),
	}

	h.mu.Lock()
	// If this cluster ID already has a connection (e.g. the agent
	// restarted and reconnected before we noticed the old one died),
	// replace it — the newest connection wins.
	h.agents[clusterID] = ac
	h.mu.Unlock()

	log.Printf("tunnel: agent connected for cluster %q", clusterID)

	// readLoop blocks for the lifetime of this connection — this
	// handler function doesn't return until the agent disconnects.
	h.readLoop(ac)

	h.mu.Lock()
	if h.agents[clusterID] == ac { // only remove if nobody's replaced us since
		delete(h.agents, clusterID)
	}
	h.mu.Unlock()
	log.Printf("tunnel: agent disconnected for cluster %q", clusterID)
}

// readLoop continuously reads JobResponse messages from the agent and
// routes each one to whichever goroutine is waiting on its JobID.
func (h *Hub) readLoop(ac *agentConn) {
	for {
		var resp JobResponse
		if err := ac.conn.ReadJSON(&resp); err != nil {
			// Connection closed, network error, or malformed JSON — any
			// of these ends this agent's session. We don't distinguish
			// further here; the caller sees the agent as disconnected
			// either way.
			return
		}

		ac.pendingMu.Lock()
		ch, ok := ac.pending[resp.JobID]
		if ok {
			delete(ac.pending, resp.JobID)
		}
		ac.pendingMu.Unlock()

		if ok {
			// Buffered channel of size 1 (see Dispatch below) means this
			// send never blocks even if the original requester already
			// gave up waiting (e.g. its own context timed out) — it just
			// writes into the buffer and the channel gets garbage
			// collected once both sides are done with it.
			ch <- resp
		}
		// If !ok, this is a response to a job whose original requester
		// already timed out and stopped listening — we simply drop it.
	}
}

// Dispatch sends a JobRequest to the named cluster's agent and blocks
// until either a matching JobResponse arrives or ctx is cancelled.
func (h *Hub) Dispatch(ctx context.Context, clusterID string, req JobRequest) (*model.DiagnosticSnapshot, error) {
	h.mu.Lock()
	ac, connected := h.agents[clusterID]
	h.mu.Unlock()

	if !connected {
		return nil, fmt.Errorf("tunnel: no agent currently connected for cluster %q", clusterID)
	}

	if req.JobID == "" {
		req.JobID = newJobID()
	}

	// Buffered with capacity 1 so readLoop's send above never blocks,
	// even in the timeout race described there.
	respCh := make(chan JobResponse, 1)
	ac.pendingMu.Lock()
	ac.pending[req.JobID] = respCh
	ac.pendingMu.Unlock()

	// websocket.Conn.WriteJSON is NOT safe for concurrent use — the
	// gorilla/websocket docs are explicit about this: at most one
	// goroutine may call any Write method at a time. Since multiple
	// Dispatch calls can run concurrently against the SAME agent
	// connection (two users troubleshooting the same cluster
	// simultaneously), we serialize writes with writeMu. Reads (in
	// readLoop) are separately safe to run concurrently with writes —
	// only writes need this protection.
	ac.writeMu.Lock()
	err := ac.conn.WriteJSON(req)
	ac.writeMu.Unlock()
	if err != nil {
		ac.pendingMu.Lock()
		delete(ac.pending, req.JobID)
		ac.pendingMu.Unlock()
		return nil, fmt.Errorf("tunnel: sending job to cluster %q: %w", clusterID, err)
	}

	select {
	case resp := <-respCh:
		if resp.Error != "" {
			return nil, fmt.Errorf("tunnel: agent error: %s", resp.Error)
		}
		return resp.Snapshot, nil
	case <-ctx.Done():
		ac.pendingMu.Lock()
		delete(ac.pending, req.JobID)
		ac.pendingMu.Unlock()
		return nil, fmt.Errorf("tunnel: waiting for agent response: %w", ctx.Err())
	}
}

// IsConnected reports whether a given cluster currently has a live
// agent connection — useful for a status endpoint in the UI.
func (h *Hub) IsConnected(clusterID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[clusterID]
	return ok
}

// VerifyToken checks whether the given token matches the registered
// secret for clusterID. This is the fix for a real gap: originally, the
// registration token only protected the AGENT's own WebSocket
// connection (ServeAgentConnect) — nothing stopped a DIFFERENT caller
// who merely knew or guessed a clusterID string from hitting
// /api/remote/{clusterId}/... and reading THAT customer's cluster data.
// internal/api's remote-cluster handlers now call this on every request
// (not just at agent-connect time), using the exact same token the
// customer received once at registration — the same secret now serves
// double duty as "proves this agent belongs here" AND "proves this API
// caller is allowed to query this cluster."
//
// Uses constant-time comparison (subtle.ConstantTimeCompare) rather
// than == — a naive string comparison returns as soon as it finds the
// first mismatched byte, and an attacker who can measure response
// timing precisely enough could theoretically use that to guess a
// token one byte at a time. Constant-time comparison always examines
// every byte regardless of where the mismatch is, closing that timing
// side-channel. This matters more here than in most equality checks
// specifically because this function's whole job is comparing a
// caller-supplied secret against a real one.
func (h *Hub) VerifyToken(clusterID, token string) bool {
	if token == "" {
		return false
	}
	h.mu.Lock()
	expected, ok := h.tokens[clusterID]
	h.mu.Unlock()
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
}

// RegisterCluster generates a fresh token for a new clusterID and
// stores it, enabling that ID to authenticate via ServeAgentConnect
// from this point forward. This is the actual self-serve "download and
// connect" backend support: a UI calls this once (POST /api/clusters)
// to get back a {clusterId, token} pair, then hands the user a
// ready-to-apply Kubernetes manifest containing both — see
// internal/api's handleRegisterCluster.
//
// Returns an error if clusterID is already registered — re-registering
// an EXISTING cluster would silently invalidate whatever token its
// already-running agent holds, disconnecting it without warning. A real
// "rotate this cluster's token" feature would be a deliberate, separate
// operation, not an accidental side effect of calling register twice.
func (h *Hub) RegisterCluster(clusterID string) (token string, err error) {
	if clusterID == "" {
		return "", fmt.Errorf("tunnel: clusterID must not be empty")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.tokens[clusterID]; exists {
		return "", fmt.Errorf("tunnel: cluster %q is already registered", clusterID)
	}

	token = newToken()
	h.tokens[clusterID] = token
	return token, nil
}

// ClusterStatus summarizes one registered cluster for a listing UI.
type ClusterStatus struct {
	ClusterID string `json:"clusterId"`
	Connected bool   `json:"connected"`
}

// ListClusters returns every registered cluster (pre-seeded or
// dynamically registered) with its current connection status — this
// powers a "your connected clusters" dashboard view, without ever
// exposing the tokens themselves back out (a token is a write-once
// secret handed to the user exactly once at registration time; if they
// lose it, they register a differently-named cluster rather than us
// re-displaying the old one).
func (h *Hub) ListClusters() []ClusterStatus {
	h.mu.Lock()
	defer h.mu.Unlock()

	statuses := make([]ClusterStatus, 0, len(h.tokens))
	for clusterID := range h.tokens {
		_, connected := h.agents[clusterID]
		statuses = append(statuses, ClusterStatus{ClusterID: clusterID, Connected: connected})
	}
	return statuses
}

func newToken() string {
	return randomHex(24) // 24 random bytes -> 48 hex chars, real entropy for a bearer secret
}

func newJobID() string {
	return randomHex(8) // 8 bytes is plenty for correlation uniqueness, doesn't need token-grade entropy
}

// randomHex is the shared implementation behind newJobID and newToken —
// they need different lengths (a correlation ID just needs to be
// unique; a bearer token needs real cryptographic entropy) but the same
// generation mechanism.
func randomHex(numBytes int) string {
	b := make([]byte, numBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
