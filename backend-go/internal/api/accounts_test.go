package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/auth"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

// newRelayServer is a multi-tenant server: relay on, local mode off,
// accounts with dev login (in-memory store).
func newRelayServer(t *testing.T) *Server {
	t.Helper()
	m, err := auth.NewManager(auth.NewMemoryStore(), auth.Config{PublicURL: "http://ui.test", DevLogin: true})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		Hub:                 tunnel.NewHub(map[string]string{"seeded": "seed-token"}),
		Auth:                m,
		AdminToken:          "admin-secret",
		RelayPublicURL:      "wss://relay.example/agent/connect",
		TroubleshootTimeout: time.Second,
	}
}

// login signs in via dev login and returns the Cookie header value.
func login(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	rec := do(t, h, "POST", "/api/auth/dev-login", `{"login":"`+name+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", name, rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "kubeassist_session" {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func register(t *testing.T, h http.Handler, cookie, id string) registerClusterResponse {
	t.Helper()
	rec := do(t, h, "POST", "/api/clusters", `{"clusterId":"`+id+`"}`, "Cookie", cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s: %d %s", id, rec.Code, rec.Body)
	}
	var reg registerClusterResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &reg)
	return reg
}

func TestRemoteRoutes_RequireSessionAndOwnership(t *testing.T) {
	s := newRelayServer(t)
	h := s.NewRouter()
	alice := login(t, h, "alice")
	bob := login(t, h, "bob")
	reg := register(t, h, alice, "alice-prod")

	// Anonymous: 401, even with the cluster's agent token — agent tokens
	// authenticate agents only.
	if rec := do(t, h, "GET", "/api/remote/alice-prod/status", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: want 401, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/remote/alice-prod/status", "", "Authorization", "Bearer "+reg.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("agent token must not authorize API calls: got %d", rec.Code)
	}

	// Owner: OK, and 503 (not a hang) while the agent is offline.
	rec := do(t, h, "GET", "/api/remote/alice-prod/status", "", "Cookie", alice)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Errorf("owner status: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/api/remote/alice-prod/namespaces", "", "Cookie", alice); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("disconnected agent: want 503, got %d", rec.Code)
	}

	// Another user: indistinguishable from a cluster that doesn't exist.
	for _, path := range []string{"/api/remote/alice-prod/pods", "/api/remote/nope/pods", "/api/remote/seeded/pods"} {
		rec := do(t, h, "GET", path, "", "Cookie", bob)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "cluster_not_found") {
			t.Errorf("bob %s: want 404 cluster_not_found, got %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, "DELETE", "/api/clusters/alice-prod", "", "Cookie", bob); rec.Code != http.StatusNotFound {
		t.Errorf("bob must not revoke alice's cluster, got %d", rec.Code)
	}

	// Each user lists only their own clusters.
	rec = do(t, h, "GET", "/api/clusters", "", "Cookie", alice)
	var list []tunnel.ClusterStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].ClusterID != "alice-prod" {
		t.Errorf("alice's list = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", "/api/clusters", "", "Cookie", bob)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("bob's list should be empty, got %s", rec.Body)
	}

	// The operator token sees and reaches everything, including
	// ownerless (pre-seeded) clusters.
	rec = do(t, h, "GET", "/api/clusters", "", "Authorization", "Bearer admin-secret")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Errorf("admin list = %s", rec.Body)
	}
	if rec := do(t, h, "GET", "/api/remote/seeded/status", "", "Authorization", "Bearer admin-secret"); rec.Code != http.StatusOK {
		t.Errorf("admin should reach seeded cluster, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/remote/seeded/status", "", "Authorization", "Bearer wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong admin token: want 401, got %d", rec.Code)
	}

	// Owner revokes; the cluster is gone for everyone and the agent
	// token stops working.
	if rec := do(t, h, "DELETE", "/api/clusters/alice-prod", "", "Cookie", alice, "Origin", "http://ui.test"); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d %s", rec.Code, rec.Body)
	}
	if s.Hub.VerifyToken("alice-prod", reg.Token) {
		t.Error("revoked agent token must stop working")
	}
	if rec := do(t, h, "GET", "/api/remote/alice-prod/status", "", "Cookie", alice); rec.Code != http.StatusNotFound {
		t.Errorf("revoked cluster: want 404, got %d", rec.Code)
	}

	// Logged out: back to 401.
	do(t, h, "POST", "/api/auth/logout", "", "Cookie", alice)
	if rec := do(t, h, "GET", "/api/clusters", "", "Cookie", alice); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: want 401, got %d", rec.Code)
	}
}

func TestRegisterCluster_ValidationAndManifest(t *testing.T) {
	s := newRelayServer(t)
	h := s.NewRouter()

	if rec := do(t, h, "POST", "/api/clusters", `{"clusterId":"prod-eu"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous registration: want 401, got %d", rec.Code)
	}
	alice := login(t, h, "alice")
	for _, bad := range []string{`{}`, `{"clusterId":"Bad_ID"}`, `{"clusterId":"local"}`, `not json`} {
		if rec := do(t, h, "POST", "/api/clusters", bad, "Cookie", alice); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", bad, rec.Code)
		}
	}
	reg := register(t, h, alice, "prod-eu")
	if reg.Token == "" || !strings.Contains(reg.Manifest, reg.Token) || !strings.Contains(reg.Manifest, "wss://relay.example/agent/connect") {
		t.Errorf("manifest should embed agent token and relay URL")
	}
	if owner, _ := s.Hub.Owner("prod-eu"); owner == "" {
		t.Error("registered cluster must be owned by the caller")
	}
	bob := login(t, h, "bob")
	if rec := do(t, h, "POST", "/api/clusters", `{"clusterId":"prod-eu"}`, "Cookie", bob); rec.Code != http.StatusConflict {
		t.Errorf("taken ID: want 409, got %d", rec.Code)
	}
	// A cross-site form/fetch carrying the cookie is refused.
	if rec := do(t, h, "POST", "/api/clusters", `{"clusterId":"evil"}`, "Cookie", alice, "Origin", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("untrusted origin: want 403, got %d", rec.Code)
	}
}

func TestRegisterCluster_PerUserCapAndRateLimit(t *testing.T) {
	s := newRelayServer(t)
	s.MaxClustersPerUser = 2
	s.RegisterLimiter = NewRateLimiter(3, time.Hour)
	h := s.NewRouter()
	alice := login(t, h, "alice")

	register(t, h, alice, "a1")
	register(t, h, alice, "a2")
	rec := do(t, h, "POST", "/api/clusters", `{"clusterId":"a3"}`, "Cookie", alice)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cluster_limit_reached") {
		t.Errorf("cap: want 403 cluster_limit_reached, got %d %s", rec.Code, rec.Body)
	}
	// That was alice's 3rd attempt this hour; the 4th is rate limited.
	rec = do(t, h, "POST", "/api/clusters", `{"clusterId":"a4"}`, "Cookie", alice)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("want 429 with Retry-After, got %d %v", rec.Code, rec.Header())
	}
	// Limits are per user.
	register(t, h, login(t, h, "bob"), "b1")
}

func TestTroubleshoot_RateLimitedPerCaller(t *testing.T) {
	s := newLocalServer(t, crashObjs()...)
	s.TroubleshootLimiter = NewRateLimiter(2, time.Minute)
	h := s.NewRouter()
	call := func(ip string) int {
		req := httptest.NewRequest("POST", "/api/pods/shop/api-1/troubleshoot", nil)
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if call("10.0.0.1") != 200 || call("10.0.0.1") != 200 {
		t.Fatal("first two calls should pass")
	}
	if code := call("10.0.0.1"); code != http.StatusTooManyRequests {
		t.Errorf("third call: want 429, got %d", code)
	}
	if code := call("10.0.0.2"); code != 200 {
		t.Errorf("other IPs have their own budget, got %d", code)
	}
}

func TestClientIP_ProxyTrust(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.18.0.5:5555"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := (&Server{}).clientIP(req); got != "172.18.0.5" {
		t.Errorf("untrusted proxy headers must be ignored, got %s", got)
	}
	if got := (&Server{TrustProxyHeaders: true}).clientIP(req); got != "203.0.113.9" {
		t.Errorf("trusted: want the proxy-appended (right-most) address, got %s", got)
	}
}

func TestParseRateLimit(t *testing.T) {
	for spec, ok := range map[string]bool{"10/h": true, "30/m": true, "5/s": true, "3/10m": true, "off": true, "0": true, "": true, "x/h": false, "10": false, "10/fortnight": false} {
		_, err := ParseRateLimit(spec)
		if (err == nil) != ok {
			t.Errorf("ParseRateLimit(%q) err=%v", spec, err)
		}
	}
	if l, _ := ParseRateLimit("off"); l != nil {
		t.Error("off must disable")
	}
	var nilLimiter *RateLimiter
	if ok, _ := nilLimiter.Allow("k"); !ok {
		t.Error("nil limiter allows everything")
	}
}

func TestHealth_ReportsAuthProviders(t *testing.T) {
	rec := do(t, newRelayServer(t).NewRouter(), "GET", "/api/health", "")
	var hr healthResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &hr)
	if len(hr.AuthProviders) != 1 || hr.AuthProviders[0] != "dev" {
		t.Errorf("authProviders = %v", hr.AuthProviders)
	}
	rec = do(t, newLocalServer(t).NewRouter(), "GET", "/api/health", "")
	if !strings.Contains(rec.Body.String(), `"authProviders":[]`) {
		t.Errorf("local-only server should report no providers: %s", rec.Body)
	}
}
