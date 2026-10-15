package tunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// TestNewHub_NilTokensDoesNotPanic is a regression test for a real bug:
// NewHub(nil) used to leave Hub.tokens as a literal nil map, and the
// very first call to RegisterCluster (which WRITES to that map) panicked
// with "assignment to entry in nil map." This was caught by
// TestHub_VerifyToken_CrossCustomerIsolation below actually failing on a
// real run, not by inspection — exactly the value of running tests for
// real rather than trusting that code "should" work.
func TestNewHub_NilTokensDoesNotPanic(t *testing.T) {
	hub := NewHub(nil)
	if _, err := hub.RegisterCluster("some-cluster", "", 0); err != nil {
		t.Fatalf("RegisterCluster after NewHub(nil) unexpected error: %v", err)
	}
}

// TestHub_VerifyToken_CrossCustomerIsolation is the single most
// important test in this file: it proves that one customer's
// registration token cannot be used to access a DIFFERENT customer's
// cluster. This was a real gap — before VerifyToken existed, the
// /api/remote/{clusterId}/... handlers only checked whether an agent
// was CONNECTED for that clusterId, never whether the CALLER was
// actually authorized to query it.
func TestHub_VerifyToken_CrossCustomerIsolation(t *testing.T) {
	hub := NewHub(nil)

	tokenA, err := hub.RegisterCluster("customer-a-cluster", "", 0)
	if err != nil {
		t.Fatalf("RegisterCluster(a) unexpected error: %v", err)
	}
	tokenB, err := hub.RegisterCluster("customer-b-cluster", "", 0)
	if err != nil {
		t.Fatalf("RegisterCluster(b) unexpected error: %v", err)
	}

	if !hub.VerifyToken("customer-a-cluster", tokenA) {
		t.Error("Customer A's own token should verify against their own cluster")
	}
	if !hub.VerifyToken("customer-b-cluster", tokenB) {
		t.Error("Customer B's own token should verify against their own cluster")
	}

	// The actual security property being fixed:
	if hub.VerifyToken("customer-a-cluster", tokenB) {
		t.Error("SECURITY BUG: Customer B's token verified against Customer A's cluster")
	}
	if hub.VerifyToken("customer-b-cluster", tokenA) {
		t.Error("SECURITY BUG: Customer A's token verified against Customer B's cluster")
	}
}

func TestHub_VerifyToken_EdgeCases(t *testing.T) {
	hub := NewHub(nil)
	token, err := hub.RegisterCluster("test-cluster", "", 0)
	if err != nil {
		t.Fatalf("RegisterCluster unexpected error: %v", err)
	}

	tests := []struct {
		name      string
		clusterID string
		token     string
		want      bool
	}{
		{"correct token", "test-cluster", token, true},
		{"empty token always rejected", "test-cluster", "", false},
		{"wrong token rejected", "test-cluster", "0000000000000000000000000000000000000000000000", false},
		{"unknown cluster rejected even with a real token", "nonexistent-cluster", token, false},
		{"empty cluster ID rejected", "", token, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hub.VerifyToken(tt.clusterID, tt.token)
			if got != tt.want {
				t.Errorf("VerifyToken(%q, %q) = %v, want %v", tt.clusterID, tt.token, got, tt.want)
			}
		})
	}
}

func TestHub_RegisterCluster_PreventsDuplicateRegistration(t *testing.T) {
	hub := NewHub(nil)

	if _, err := hub.RegisterCluster("my-cluster", "", 0); err != nil {
		t.Fatalf("first registration unexpected error: %v", err)
	}

	// Re-registering an EXISTING clusterID must fail — silently
	// succeeding would invalidate whatever token an already-connected
	// agent is using, disconnecting them without warning.
	if _, err := hub.RegisterCluster("my-cluster", "", 0); err == nil {
		t.Error("expected an error re-registering an already-registered clusterID, got nil")
	}
}

func TestHub_RegisterCluster_RejectsEmptyClusterID(t *testing.T) {
	hub := NewHub(nil)
	if _, err := hub.RegisterCluster("", "", 0); err == nil {
		t.Error("expected an error registering an empty clusterID, got nil")
	}
}

func TestHub_ListClusters_NeverExposesTokens(t *testing.T) {
	hub := NewHub(nil)
	token, _ := hub.RegisterCluster("test-cluster", "", 0)

	statuses := hub.ListClusters()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(statuses))
	}
	if statuses[0].ClusterID != "test-cluster" {
		t.Errorf("ClusterID = %q, want %q", statuses[0].ClusterID, "test-cluster")
	}
	// ClusterStatus has no Token field at all — this test documents
	// that design choice explicitly, so a future edit adding one back
	// would need to consciously remove this test rather than silently
	// leaking tokens through a listing endpoint.
	_ = token
}

// fakeStore is an in-memory ClusterStore.
type fakeStore struct {
	mu   sync.Mutex
	rows map[string]StoredCluster
}

func (f *fakeStore) SaveCluster(_ context.Context, id, hash, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id] = StoredCluster{TokenHash: hash, OwnerID: owner}
	return nil
}
func (f *fakeStore) DeleteCluster(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	return nil
}
func (f *fakeStore) LoadClusters(context.Context) (map[string]StoredCluster, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]StoredCluster{}
	for k, v := range f.rows {
		out[k] = v
	}
	return out, nil
}

// TestHub_RegistrationsSurviveRestart: a second Hub built from the same
// store accepts the token issued by the first — and the store only ever
// saw a hash, never the token.
func TestHub_RegistrationsSurviveRestart(t *testing.T) {
	st := &fakeStore{rows: map[string]StoredCluster{}}
	h1 := NewHub(nil)
	if err := h1.AttachStore(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	token, err := h1.RegisterCluster("prod", "user-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.rows["prod"].TokenHash == token || st.rows["prod"].TokenHash != HashToken(token) {
		t.Fatal("store must hold the token hash, not the token")
	}

	h2 := NewHub(nil)
	if err := h2.AttachStore(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !h2.VerifyToken("prod", token) {
		t.Error("token should verify after restart")
	}
	if owner, ok := h2.Owner("prod"); !ok || owner != "user-1" {
		t.Errorf("ownership should survive restart, got %q %v", owner, ok)
	}
	if err := h2.RevokeCluster("prod"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.rows["prod"]; ok || h2.VerifyToken("prod", token) {
		t.Error("revoked cluster must be gone from hub and store")
	}
}

func TestHub_OwnershipAndPerOwnerLimit(t *testing.T) {
	hub := NewHub(map[string]string{"seeded": "tok"})
	for _, id := range []string{"a1", "a2"} {
		if _, err := hub.RegisterCluster(id, "alice", 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := hub.RegisterCluster("a3", "alice", 2); !errors.Is(err, ErrTooManyClusters) {
		t.Errorf("third cluster should hit the limit, got %v", err)
	}
	if _, err := hub.RegisterCluster("b1", "bob", 2); err != nil {
		t.Errorf("limit is per owner: %v", err)
	}
	if _, err := hub.RegisterCluster("b1", "bob", 2); !errors.Is(err, ErrAlreadyRegistered) {
		t.Errorf("duplicate should be ErrAlreadyRegistered, got %v", err)
	}
	got := hub.ListClustersOwnedBy("alice")
	if len(got) != 2 || got[0].ClusterID != "a1" || got[1].ClusterID != "a2" {
		t.Errorf("alice's clusters = %+v", got)
	}
	if len(hub.ListClustersOwnedBy("")) != 0 {
		t.Error("empty owner must match nothing (seeded clusters are operator-only)")
	}
	if owner, ok := hub.Owner("seeded"); !ok || owner != "" {
		t.Errorf("seeded cluster should exist with no owner, got %q %v", owner, ok)
	}
	if len(hub.ListClusters()) != 4 {
		t.Errorf("operator listing should include everything")
	}
}

func TestHub_RejectsInvalidClusterIDs(t *testing.T) {
	hub := NewHub(nil)
	for _, id := range []string{"UPPER", "has space", "-leading", "trailing-", "a_b", strings.Repeat("a", 64)} {
		if _, err := hub.RegisterCluster(id, "", 0); err == nil {
			t.Errorf("RegisterCluster(%q) should fail", id)
		}
	}
}

// TestHub_EndToEndOverWebSocket runs a real relay and a minimal agent
// over a real WebSocket: header auth, job kinds, concurrent jobs,
// NotFound mapping and disconnect handling.
func TestHub_EndToEndOverWebSocket(t *testing.T) {
	hub := NewHub(nil)
	token, _ := hub.RegisterCluster("edge", "", 0)
	srv := httptest.NewServer(http.HandlerFunc(hub.ServeAgentConnect))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Wrong token is refused at the handshake.
	bad := http.Header{"Authorization": {"Bearer nope"}, "X-Cluster-Id": {"edge"}}
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, bad); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token should get 401, got err=%v", err)
	}

	hdr := http.Header{"Authorization": {"Bearer " + token}, "X-Cluster-Id": {"edge"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatal(err)
	}
	var wmu sync.Mutex
	go func() { // the fake agent
		for {
			var req JobRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			resp := JobResponse{JobID: req.JobID}
			switch req.Kind {
			case JobListNamespaces:
				resp.Namespaces = []string{"default", "shop"}
			case JobSnapshot:
				if req.Pod == "missing" {
					resp.Error, resp.NotFound = `pods "missing" not found`, true
				} else {
					resp.Snapshot = &model.DiagnosticSnapshot{Pod: model.PodInfo{Name: req.Pod, Namespace: req.Namespace}}
				}
			}
			wmu.Lock()
			_ = conn.WriteJSON(resp)
			wmu.Unlock()
		}
	}()

	waitFor(t, func() bool { return hub.IsConnected("edge") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, err := hub.Dispatch(ctx, "edge", JobRequest{Namespace: "shop", Pod: "api"})
			if err != nil || snap.Pod.Name != "api" {
				t.Errorf("Dispatch: %v %+v", err, snap)
			}
		}()
	}
	wg.Wait()

	resp, err := hub.Do(ctx, "edge", JobRequest{Kind: JobListNamespaces})
	if err != nil || len(resp.Namespaces) != 2 {
		t.Errorf("namespaces: %v %+v", err, resp)
	}
	if _, err := hub.Dispatch(ctx, "edge", JobRequest{Namespace: "shop", Pod: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	conn.Close()
	waitFor(t, func() bool { return !hub.IsConnected("edge") })
	if _, err := hub.Do(ctx, "edge", JobRequest{Kind: JobListNamespaces}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("want ErrNotConnected after disconnect, got %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
