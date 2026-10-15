package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

const (
	pingInterval = 30 * time.Second
	pongWait     = 75 * time.Second
	writeWait    = 10 * time.Second
	// maxMessageBytes bounds a single agent message. Snapshots are
	// capped at 32KB of logs plus metadata, so 8MB is generous.
	maxMessageBytes = 8 << 20
)

// ErrNotConnected is returned when no agent is connected for a cluster.
var ErrNotConnected = errors.New("no agent currently connected for this cluster")

// ErrNotFound is returned when the agent reports the requested object
// doesn't exist in its cluster.
var ErrNotFound = errors.New("not found in remote cluster")

// clusterIDPattern restricts cluster IDs to DNS-label-like strings, so
// they're safe in URLs, logs and Kubernetes resource names.
var clusterIDPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// ValidClusterID reports whether id is an acceptable cluster ID.
func ValidClusterID(id string) bool { return clusterIDPattern.MatchString(id) }

// ClusterStore persists cluster registrations so they survive a backend
// restart. Only a SHA-256 hash of each token is stored — a leaked
// database dump does not hand out working agent credentials.
// internal/store implements this against Postgres.
type ClusterStore interface {
	SaveCluster(ctx context.Context, clusterID, tokenHash string) error
	DeleteCluster(ctx context.Context, clusterID string) error
	LoadClusters(ctx context.Context) (map[string]string, error)
}

// Hub tracks every currently-connected agent (one per customer cluster)
// and lets the rest of the backend dispatch jobs to a specific one by
// cluster ID. It is the shared hand-off point between HTTP request
// goroutines and the goroutines reading each agent's WebSocket.
type Hub struct {
	mu     sync.Mutex
	agents map[string]*agentConn // keyed by ClusterID

	// tokenHashes maps ClusterID -> hex(sha256(token)). Pre-seeded from
	// AGENT_TOKENS, loaded from the ClusterStore, and extended at runtime
	// by RegisterCluster.
	tokenHashes map[string]string

	store    ClusterStore
	upgrader websocket.Upgrader
}

// agentConn wraps one agent's live WebSocket connection plus the
// bookkeeping needed to route JobResponses back to whichever goroutine
// is waiting for them.
type agentConn struct {
	clusterID   string
	conn        *websocket.Conn
	connectedAt time.Time

	writeMu sync.Mutex // websocket connections allow only one concurrent writer

	pendingMu sync.Mutex
	pending   map[string]chan JobResponse // keyed by JobID
}

// NewHub constructs a Hub from a static clusterID->token map (parsed
// from AGENT_TOKENS), which may be nil when every cluster registers
// dynamically via RegisterCluster.
func NewHub(tokens map[string]string) *Hub {
	hashes := make(map[string]string, len(tokens))
	for id, tok := range tokens {
		hashes[id] = HashToken(tok)
	}
	return &Hub{
		agents:      make(map[string]*agentConn),
		tokenHashes: hashes,
		upgrader: websocket.Upgrader{
			// Agents aren't browsers, so the browser same-origin check
			// doesn't apply; the bearer token is the security boundary.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// AttachStore loads persisted registrations and makes future
// RegisterCluster/RevokeCluster calls durable. Pre-seeded AGENT_TOKENS
// entries win over stored ones with the same ID.
func (h *Hub) AttachStore(ctx context.Context, store ClusterStore) error {
	stored, err := store.LoadClusters(ctx)
	if err != nil {
		return fmt.Errorf("tunnel: loading persisted clusters: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, hash := range stored {
		if _, seeded := h.tokenHashes[id]; !seeded {
			h.tokenHashes[id] = hash
		}
	}
	h.store = store
	return nil
}

// HashToken returns the hex SHA-256 of a token. Tokens are 192-bit
// random values, so a fast unsalted hash is appropriate (there is no
// low-entropy password to brute-force).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// agentCredentials extracts the cluster ID and token from an agent's
// upgrade request. The Authorization header is preferred because query
// strings end up in proxy access logs; query params remain accepted so
// agents built before the header existed can still connect.
func agentCredentials(r *http.Request) (clusterID, token string) {
	clusterID = r.Header.Get("X-Cluster-ID")
	if clusterID == "" {
		clusterID = r.URL.Query().Get("clusterId")
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token = strings.TrimPrefix(h, "Bearer ")
	} else {
		token = r.URL.Query().Get("token")
	}
	return clusterID, token
}

// ServeAgentConnect handles GET /agent/connect. The connection stays
// open for the agent's entire lifetime.
func (h *Hub) ServeAgentConnect(w http.ResponseWriter, r *http.Request) {
	clusterID, token := agentCredentials(r)
	if !h.VerifyToken(clusterID, token) {
		http.Error(w, "invalid cluster id or token", http.StatusUnauthorized)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("tunnel: websocket upgrade failed for cluster %q: %v", clusterID, err)
		return
	}
	conn.SetReadLimit(maxMessageBytes)

	ac := &agentConn{
		clusterID:   clusterID,
		conn:        conn,
		connectedAt: time.Now().UTC(),
		pending:     make(map[string]chan JobResponse),
	}

	h.mu.Lock()
	// If this cluster already has a connection (the agent restarted and
	// reconnected before we noticed the old one died), the newest wins.
	if old := h.agents[clusterID]; old != nil {
		_ = old.conn.Close()
	}
	h.agents[clusterID] = ac
	h.mu.Unlock()

	log.Printf("tunnel: agent connected for cluster %q", clusterID)

	stopPing := make(chan struct{})
	go ac.pingLoop(stopPing)

	// readLoop blocks for the lifetime of this connection.
	ac.readLoop()
	close(stopPing)
	_ = conn.Close()

	h.mu.Lock()
	if h.agents[clusterID] == ac { // only remove if nobody's replaced us since
		delete(h.agents, clusterID)
	}
	h.mu.Unlock()
	ac.failPending("agent disconnected")
	log.Printf("tunnel: agent disconnected for cluster %q", clusterID)
}

// pingLoop keeps NAT/load-balancer idle timeouts from silently killing
// the connection, and lets readLoop notice a dead peer via pongWait.
func (ac *agentConn) pingLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ac.writeMu.Lock()
			err := ac.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
			ac.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// readLoop continuously reads JobResponse messages from the agent and
// routes each one to whichever goroutine is waiting on its JobID.
func (ac *agentConn) readLoop() {
	_ = ac.conn.SetReadDeadline(time.Now().Add(pongWait))
	ac.conn.SetPongHandler(func(string) error {
		return ac.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		var resp JobResponse
		if err := ac.conn.ReadJSON(&resp); err != nil {
			return
		}
		_ = ac.conn.SetReadDeadline(time.Now().Add(pongWait))

		ac.pendingMu.Lock()
		ch, ok := ac.pending[resp.JobID]
		if ok {
			delete(ac.pending, resp.JobID)
		}
		ac.pendingMu.Unlock()

		// Buffered channel of size 1 means this never blocks even if the
		// requester already gave up; responses to abandoned jobs are dropped.
		if ok {
			ch <- resp
		}
	}
}

// failPending unblocks every in-flight Dispatch when the agent goes
// away, instead of leaving them to wait out their full timeout.
func (ac *agentConn) failPending(reason string) {
	ac.pendingMu.Lock()
	defer ac.pendingMu.Unlock()
	for id, ch := range ac.pending {
		ch <- JobResponse{JobID: id, Error: reason}
		delete(ac.pending, id)
	}
}

// Do sends a JobRequest to the named cluster's agent and blocks until a
// matching JobResponse arrives or ctx is cancelled.
func (h *Hub) Do(ctx context.Context, clusterID string, req JobRequest) (*JobResponse, error) {
	h.mu.Lock()
	ac, connected := h.agents[clusterID]
	h.mu.Unlock()
	if !connected {
		return nil, fmt.Errorf("tunnel: cluster %q: %w", clusterID, ErrNotConnected)
	}

	if req.JobID == "" {
		req.JobID = newJobID()
	}
	if req.Kind == "" {
		req.Kind = JobSnapshot
	}

	respCh := make(chan JobResponse, 1)
	ac.pendingMu.Lock()
	ac.pending[req.JobID] = respCh
	ac.pendingMu.Unlock()

	ac.writeMu.Lock()
	_ = ac.conn.SetWriteDeadline(time.Now().Add(writeWait))
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
		if resp.NotFound {
			return nil, fmt.Errorf("tunnel: %s: %w", resp.Error, ErrNotFound)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("tunnel: agent error: %s", resp.Error)
		}
		return &resp, nil
	case <-ctx.Done():
		ac.pendingMu.Lock()
		delete(ac.pending, req.JobID)
		ac.pendingMu.Unlock()
		return nil, fmt.Errorf("tunnel: waiting for agent response: %w", ctx.Err())
	}
}

// Dispatch is the snapshot-collection shorthand for Do.
func (h *Hub) Dispatch(ctx context.Context, clusterID string, req JobRequest) (*model.DiagnosticSnapshot, error) {
	req.Kind = JobSnapshot
	resp, err := h.Do(ctx, clusterID, req)
	if err != nil {
		return nil, err
	}
	if resp.Snapshot == nil {
		return nil, fmt.Errorf("tunnel: agent returned no snapshot")
	}
	return resp.Snapshot, nil
}

// IsConnected reports whether a cluster currently has a live agent.
func (h *Hub) IsConnected(clusterID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[clusterID]
	return ok
}

// VerifyToken checks a caller-supplied token against clusterID's
// registered token. The same secret serves double duty: it proves an
// agent belongs to a cluster AND authorizes API callers to query that
// cluster via /api/remote/{clusterId}/..., which is what stops one
// customer reading another's cluster.
//
// The comparison is constant-time over the hashes, closing the timing
// side-channel a naive == would leave.
func (h *Hub) VerifyToken(clusterID, token string) bool {
	if token == "" || clusterID == "" {
		return false
	}
	h.mu.Lock()
	expected, ok := h.tokenHashes[clusterID]
	h.mu.Unlock()
	if !ok || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(HashToken(token))) == 1
}

// RegisterCluster generates a fresh token for a new clusterID, persists
// its hash if a store is attached, and returns the plaintext token —
// the only time it is ever available.
//
// Re-registering an existing ID is refused: it would silently invalidate
// the token a running agent holds. Rotation is RevokeCluster + register.
func (h *Hub) RegisterCluster(clusterID string) (token string, err error) {
	if clusterID == "" {
		return "", fmt.Errorf("tunnel: clusterID must not be empty")
	}
	if !ValidClusterID(clusterID) {
		return "", fmt.Errorf("tunnel: clusterID must be lowercase letters, digits and '-', 1-63 chars")
	}

	h.mu.Lock()
	if _, exists := h.tokenHashes[clusterID]; exists {
		h.mu.Unlock()
		return "", fmt.Errorf("tunnel: cluster %q is already registered", clusterID)
	}
	token = newToken()
	hash := HashToken(token)
	h.tokenHashes[clusterID] = hash
	store := h.store
	h.mu.Unlock()

	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.SaveCluster(ctx, clusterID, hash); err != nil {
			h.mu.Lock()
			delete(h.tokenHashes, clusterID)
			h.mu.Unlock()
			return "", fmt.Errorf("tunnel: persisting cluster %q: %w", clusterID, err)
		}
	}
	return token, nil
}

// RevokeCluster removes a registration and disconnects its agent.
func (h *Hub) RevokeCluster(clusterID string) error {
	h.mu.Lock()
	if _, ok := h.tokenHashes[clusterID]; !ok {
		h.mu.Unlock()
		return fmt.Errorf("tunnel: cluster %q is not registered", clusterID)
	}
	delete(h.tokenHashes, clusterID)
	ac := h.agents[clusterID]
	delete(h.agents, clusterID)
	store := h.store
	h.mu.Unlock()

	if ac != nil {
		_ = ac.conn.Close()
	}
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.DeleteCluster(ctx, clusterID); err != nil {
			return fmt.Errorf("tunnel: deleting persisted cluster %q: %w", clusterID, err)
		}
	}
	return nil
}

// ClusterStatus summarizes one registered cluster for a listing UI.
type ClusterStatus struct {
	ClusterID   string     `json:"clusterId"`
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
}

// Status returns one cluster's status.
func (h *Hub) Status(clusterID string) ClusterStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := ClusterStatus{ClusterID: clusterID}
	if ac, ok := h.agents[clusterID]; ok {
		st.Connected = true
		t := ac.connectedAt
		st.ConnectedAt = &t
	}
	return st
}

// ListClusters returns every registered cluster with its connection
// status — never the tokens (or their hashes).
func (h *Hub) ListClusters() []ClusterStatus {
	h.mu.Lock()
	ids := make([]string, 0, len(h.tokenHashes))
	for id := range h.tokenHashes {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	sort.Strings(ids)

	statuses := make([]ClusterStatus, 0, len(ids))
	for _, id := range ids {
		statuses = append(statuses, h.Status(id))
	}
	return statuses
}

func newToken() string {
	return randomHex(24) // 192 bits — real entropy for a bearer secret
}

func newJobID() string {
	return randomHex(8) // a correlation ID only needs uniqueness
}

func randomHex(numBytes int) string {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		panic("tunnel: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
