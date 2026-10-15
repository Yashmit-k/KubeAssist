package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func newTestManager(t *testing.T, cfg Config) (*Manager, *http.ServeMux) {
	t.Helper()
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://ui.test"
	}
	m, err := NewManager(NewMemoryStore(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterRoutes(mux, nil)
	return m, mux
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie set (status %d, body %s)", name, rec.Code, rec.Body)
	return nil
}

func TestNewManager_Validation(t *testing.T) {
	if _, err := NewManager(NewMemoryStore(), Config{PublicURL: "not a url"}); err == nil {
		t.Error("relative PUBLIC_URL must be rejected")
	}
	if _, err := NewManager(NewMemoryStore(), Config{PublicURL: "https://app.example.com", DevLogin: true}); err == nil {
		t.Error("dev login on https must be refused")
	}
	if _, err := NewManager(NewMemoryStore(), Config{PublicURL: "https://app.example.com", GitHub: &GitHubConfig{ClientID: "x"}}); err == nil {
		t.Error("GitHub without a secret must be refused")
	}
	m, err := NewManager(NewMemoryStore(), Config{PublicURL: "https://app.example.com", GitHub: &GitHubConfig{ClientID: "x", ClientSecret: "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if m.cookieName != "__Host-kubeassist_session" || !m.secure {
		t.Errorf("https deployments must use a Secure __Host- cookie, got %q secure=%v", m.cookieName, m.secure)
	}
	if got := m.Providers(); len(got) != 1 || got[0] != ProviderGitHub {
		t.Errorf("providers = %v", got)
	}
}

func TestDevLogin_SessionLifecycle(t *testing.T) {
	m, mux := newTestManager(t, Config{DevLogin: true})

	req := httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"login":"Alice"}`))
	rec := serve(mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dev login: %d %s", rec.Code, rec.Body)
	}
	c := sessionCookie(t, rec, "kubeassist_session")
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("session cookie must be HttpOnly, SameSite=Lax, Path=/: %+v", c)
	}

	me := httptest.NewRequest("GET", "/api/auth/me", nil)
	me.AddCookie(c)
	rec = serve(mux, me)
	var u struct{ ID, Login, Provider string }
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if rec.Code != http.StatusOK || u.Login != "alice" || u.Provider != ProviderDev || u.ID == "" {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}

	// Same login -> same user.
	rec = serve(mux, httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"login":"alice"}`)))
	var again struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.ID != u.ID {
		t.Errorf("re-login should return the same user, got %q vs %q", again.ID, u.ID)
	}

	// Logout from a foreign origin is refused; from our own it works.
	out := httptest.NewRequest("POST", "/api/auth/logout", nil)
	out.Header.Set("Origin", "https://evil.example")
	out.AddCookie(c)
	if rec := serve(mux, out); rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin logout: want 403, got %d", rec.Code)
	}
	out = httptest.NewRequest("POST", "/api/auth/logout", nil)
	out.Header.Set("Origin", "http://ui.test")
	out.AddCookie(c)
	if rec := serve(mux, out); rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	me = httptest.NewRequest("GET", "/api/auth/me", nil)
	me.AddCookie(c)
	if rec := serve(mux, me); rec.Code != http.StatusUnauthorized {
		t.Errorf("session must be dead after logout, got %d", rec.Code)
	}

	for _, bad := range []string{`{"login":""}`, `{"login":"a b"}`, `{"login":"` + strings.Repeat("a", 40) + `"}`, `nope`} {
		if rec := serve(mux, httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(bad))); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", bad, rec.Code)
		}
	}
	_ = m
}

func TestDevLogin_DisabledByDefault(t *testing.T) {
	_, mux := newTestManager(t, Config{})
	if rec := serve(mux, httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"login":"a"}`))); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("dev login route must not exist unless enabled, got %d", rec.Code)
	}
}

func TestCurrentUser_RejectsUntrustedOriginForWrites(t *testing.T) {
	m, mux := newTestManager(t, Config{DevLogin: true, TrustedOrigins: []string{"http://localhost:5173"}})
	c := sessionCookie(t, serve(mux, httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"login":"bob"}`))), "kubeassist_session")

	cases := []struct {
		method, origin string
		ok             bool
	}{
		{"GET", "https://evil.example", true}, // reads are protected by CORS, not this
		{"POST", "", true},
		{"POST", "http://ui.test", true},
		{"POST", "http://localhost:5173", true},
		{"POST", "http://api.internal", true}, // same host as the request
		{"DELETE", "https://evil.example", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, "http://api.internal/api/clusters", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		req.AddCookie(c)
		u, err := m.CurrentUser(req)
		if tc.ok && (err != nil || u == nil || u.Login != "bob") {
			t.Errorf("%s from %q: want bob, got %v %v", tc.method, tc.origin, u, err)
		}
		if !tc.ok && err != ErrUntrustedOrigin {
			t.Errorf("%s from %q: want ErrUntrustedOrigin, got %v %v", tc.method, tc.origin, u, err)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "/",
		"/connect":              "/connect",
		"/c/prod/pods?ns=a":     "/c/prod/pods?ns=a",
		"//evil.example":        "/",
		"/\\evil.example":       "/",
		"https://evil.example/": "/",
		"connect":               "/",
		"/x\r\nSet-Cookie: a=b": "/",
	} {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeGitHub implements the three GitHub endpoints the flow uses.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"gho_test","token_type":"bearer"}`))
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":4242,"login":"octocat","name":"The Octocat","avatar_url":"https://avatars.example/u/4242"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubFlow(t *testing.T) {
	gh := fakeGitHub(t)
	_, mux := newTestManager(t, Config{GitHub: &GitHubConfig{
		ClientID: "cid", ClientSecret: "secret",
		Endpoint:   oauth2.Endpoint{AuthURL: gh.URL + "/login/oauth/authorize", TokenURL: gh.URL + "/login/oauth/access_token"},
		APIBaseURL: gh.URL,
	}})

	// 1. Start: redirect to GitHub with state + PKCE, cookie set.
	rec := serve(mux, httptest.NewRequest("GET", "/api/auth/github/login?next=/connect", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login start: %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	state := loc.Query().Get("state")
	if !strings.HasPrefix(loc.String(), gh.URL+"/login/oauth/authorize") || state == "" ||
		loc.Query().Get("code_challenge_method") != "S256" || loc.Query().Get("redirect_uri") != "http://ui.test/api/auth/github/callback" {
		t.Fatalf("bad authorize redirect %s", loc)
	}
	oc := sessionCookie(t, rec, "kubeassist_oauth")

	callback := func(query string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/auth/github/callback?"+query, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		return serve(mux, req)
	}

	// 2. Forged state or missing cookie never logs anyone in.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"wrong state": callback("code=good-code&state=forged", oc),
		"no cookie":   callback("code=good-code&state="+state, nil),
		"denied":      callback("error=access_denied&state="+state, oc),
		"bad code":    callback("code=bad&state="+state, oc),
	} {
		if !strings.HasPrefix(rec.Header().Get("Location"), "http://ui.test/connect?authError=") {
			t.Errorf("%s: want redirect with authError, got %d %q", name, rec.Code, rec.Header().Get("Location"))
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == "kubeassist_session" && c.Value != "" {
				t.Errorf("%s: must not set a session", name)
			}
		}
	}

	// 3. The real callback signs the user in and returns to `next`.
	rec = callback("code=good-code&state="+state, oc)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "http://ui.test/connect" {
		t.Fatalf("callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	sc := sessionCookie(t, rec, "kubeassist_session")
	me := httptest.NewRequest("GET", "/api/auth/me", nil)
	me.AddCookie(sc)
	rec = serve(mux, me)
	var u struct{ Login, Provider, Name, AvatarURL string }
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if u.Login != "octocat" || u.Provider != ProviderGitHub || u.Name != "The Octocat" || u.AvatarURL == "" {
		t.Errorf("me after github login = %d %s", rec.Code, rec.Body)
	}
}
