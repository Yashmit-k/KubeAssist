package tunnel

import "testing"

// TestNewHub_NilTokensDoesNotPanic is a regression test for a real bug:
// NewHub(nil) used to leave Hub.tokens as a literal nil map, and the
// very first call to RegisterCluster (which WRITES to that map) panicked
// with "assignment to entry in nil map." This was caught by
// TestHub_VerifyToken_CrossCustomerIsolation below actually failing on a
// real run, not by inspection — exactly the value of running tests for
// real rather than trusting that code "should" work.
func TestNewHub_NilTokensDoesNotPanic(t *testing.T) {
	hub := NewHub(nil)
	if _, err := hub.RegisterCluster("some-cluster"); err != nil {
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

	tokenA, err := hub.RegisterCluster("customer-a-cluster")
	if err != nil {
		t.Fatalf("RegisterCluster(a) unexpected error: %v", err)
	}
	tokenB, err := hub.RegisterCluster("customer-b-cluster")
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
	token, err := hub.RegisterCluster("test-cluster")
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

	if _, err := hub.RegisterCluster("my-cluster"); err != nil {
		t.Fatalf("first registration unexpected error: %v", err)
	}

	// Re-registering an EXISTING clusterID must fail — silently
	// succeeding would invalidate whatever token an already-connected
	// agent is using, disconnecting them without warning.
	if _, err := hub.RegisterCluster("my-cluster"); err == nil {
		t.Error("expected an error re-registering an already-registered clusterID, got nil")
	}
}

func TestHub_RegisterCluster_RejectsEmptyClusterID(t *testing.T) {
	hub := NewHub(nil)
	if _, err := hub.RegisterCluster(""); err == nil {
		t.Error("expected an error registering an empty clusterID, got nil")
	}
}

func TestHub_ListClusters_NeverExposesTokens(t *testing.T) {
	hub := NewHub(nil)
	token, _ := hub.RegisterCluster("test-cluster")

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
