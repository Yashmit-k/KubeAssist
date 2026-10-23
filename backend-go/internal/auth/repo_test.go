package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
)

// repoGitHub fakes GitHub for repository access: three codes map to
// three tokens; gho_other belongs to a different GitHub account.
type repoGitHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	revoked []string
}

func newRepoGitHub(t *testing.T) *repoGitHub {
	t.Helper()
	g := &repoGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tok := map[string]string{"good-code": "gho_signin", "repo-code": "gho_repo", "other-code": "gho_other"}[r.Form.Get("code")]
		if tok == "" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + tok + `","token_type":"bearer"}`))
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer gho_signin":
			_, _ = w.Write([]byte(`{"id":4242,"login":"octocat"}`))
		case "Bearer gho_repo":
			w.Header().Set("X-OAuth-Scopes", "public_repo")
			_, _ = w.Write([]byte(`{"id":4242,"login":"octocat"}`))
		case "Bearer gho_other":
			_, _ = w.Write([]byte(`{"id":999,"login":"someone-else"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	mux.HandleFunc("DELETE /applications/cid/token", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if user == "cid" && pass == "secret" {
			g.mu.Lock()
			g.revoked = append(g.revoked, body["access_token"])
			g.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func repoManager(t *testing.T, g *repoGitHub) (*Manager, *http.ServeMux, *MemoryStore) {
	t.Helper()
	st := NewMemoryStore()
	m, err := NewManager(st, Config{
		PublicURL: "http://ui.test", DevLogin: true,
		GitHub: &GitHubConfig{ClientID: "cid", ClientSecret: "secret",
			Endpoint:   oauth2.Endpoint{AuthURL: g.srv.URL + "/login/oauth/authorize", TokenURL: g.srv.URL + "/login/oauth/access_token"},
			APIBaseURL: g.srv.URL},
		TokenKey: []byte("0123456789abcdef0123456789abcdef"), TokenStore: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.RegisterRoutes(mux, nil)
	return m, mux, st
}

// githubSession signs in through the (fake) GitHub flow and returns the session cookie.
func githubSession(t *testing.T, mux *http.ServeMux) *http.Cookie {
	t.Helper()
	rec := serve(mux, httptest.NewRequest("GET", "/api/auth/github/login", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	oc := sessionCookie(t, rec, "kubeassist_oauth")
	req := httptest.NewRequest("GET", "/api/auth/github/callback?code=good-code&state="+loc.Query().Get("state"), nil)
	req.AddCookie(oc)
	return sessionCookie(t, serve(mux, req), "kubeassist_session")
}

// connectRepo runs the repo-access flow with the given code.
func connectRepo(t *testing.T, mux *http.ServeMux, session *http.Cookie, code, private string) (authorize *url.URL, final *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/auth/github/repo-access/start?private="+private+"&next=/c/prod/pods", nil)
	req.AddCookie(session)
	rec := serve(mux, req)
	authorize, _ = url.Parse(rec.Header().Get("Location"))
	rc := sessionCookie(t, rec, "kubeassist_oauth_repo")
	cb := httptest.NewRequest("GET", "/api/auth/github/callback/repo?code="+code+"&state="+authorize.Query().Get("state"), nil)
	cb.AddCookie(session)
	cb.AddCookie(rc)
	return authorize, serve(mux, cb)
}

func TestRepoAccess_ConnectStoresSealedTokenAndDisconnectRevokes(t *testing.T) {
	g := newRepoGitHub(t)
	m, mux, st := repoManager(t, g)
	session := githubSession(t, mux)

	authorize, rec := connectRepo(t, mux, session, "repo-code", "0")
	if got := authorize.Query().Get("scope"); got != "public_repo" {
		t.Errorf("default scope must be public_repo, got %q", got)
	}
	if authorize.Query().Get("redirect_uri") != "http://ui.test/api/auth/github/callback/repo" {
		t.Errorf("redirect_uri = %q", authorize.Query().Get("redirect_uri"))
	}
	if loc := rec.Header().Get("Location"); loc != "http://ui.test/c/prod/pods?repoAccess=connected" {
		t.Fatalf("callback redirect = %d %q", rec.Code, loc)
	}

	me, _ := m.store.SessionUser(t.Context(), hashToken(session.Value))
	sealed, scopes, _ := st.GitHubToken(t.Context(), me.ID)
	if scopes != "public_repo" || strings.Contains(string(sealed), "gho_repo") {
		t.Fatalf("token must be stored sealed (scopes %q)", scopes)
	}
	if tok, _, err := m.GitHubToken(t.Context(), me.ID); err != nil || tok != "gho_repo" {
		t.Fatalf("GitHubToken = %q %v", tok, err)
	}
	if _, err := m.open(sealed, "another-user"); err == nil {
		t.Error("a sealed token must not open for another user")
	}

	status := httptest.NewRequest("GET", "/api/auth/github/repo-access", nil)
	status.AddCookie(session)
	if body := serve(mux, status).Body.String(); !strings.Contains(body, `"connected":true`) || !strings.Contains(body, `"private":false`) {
		t.Errorf("status = %s", body)
	}

	// The private-repos option asks for the repo scope.
	authorize, _ = connectRepo(t, mux, session, "repo-code", "1")
	if authorize.Query().Get("scope") != "repo" {
		t.Errorf("private scope = %q", authorize.Query().Get("scope"))
	}

	del := httptest.NewRequest("DELETE", "/api/auth/github/repo-access", nil)
	del.AddCookie(session)
	if rec := serve(mux, del); rec.Code != http.StatusNoContent {
		t.Fatalf("disconnect: %d", rec.Code)
	}
	if _, _, err := m.GitHubToken(t.Context(), me.ID); err != ErrNoRepoAccess {
		t.Errorf("token must be gone, got %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.revoked) != 1 || g.revoked[0] != "gho_repo" {
		t.Errorf("token must be revoked on GitHub, revoked = %v", g.revoked)
	}
}

func TestRepoAccess_RefusesAnotherGitHubAccount(t *testing.T) {
	g := newRepoGitHub(t)
	m, mux, _ := repoManager(t, g)
	session := githubSession(t, mux)
	_, rec := connectRepo(t, mux, session, "other-code", "0")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "repoAccessError=account_mismatch") {
		t.Fatalf("mismatched account must be refused, got %q", loc)
	}
	me, _ := m.store.SessionUser(t.Context(), hashToken(session.Value))
	if _, _, err := m.GitHubToken(t.Context(), me.ID); err != ErrNoRepoAccess {
		t.Error("nothing may be stored for a mismatched account")
	}
}

func TestRepoAccess_NeedsAGitHubSignIn(t *testing.T) {
	g := newRepoGitHub(t)
	_, mux, _ := repoManager(t, g)
	// Signed out.
	rec := serve(mux, httptest.NewRequest("GET", "/api/auth/github/repo-access/start?next=/x", nil))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "authError=login_required") {
		t.Errorf("signed out: %q", loc)
	}
	// Dev-login users have no GitHub identity to bind a token to.
	dev := sessionCookie(t, serve(mux, httptest.NewRequest("POST", "/api/auth/dev-login", strings.NewReader(`{"login":"dev"}`))), "kubeassist_session")
	req := httptest.NewRequest("GET", "/api/auth/github/repo-access/start?next=/x", nil)
	req.AddCookie(dev)
	if loc := serve(mux, req).Header().Get("Location"); loc != "http://ui.test/x?repoAccessError=github_account_required" {
		t.Errorf("dev user: %q", loc)
	}
}

func TestRepoAccess_DisabledWithoutKey(t *testing.T) {
	m, err := NewManager(NewMemoryStore(), Config{PublicURL: "http://ui.test", GitHub: &GitHubConfig{ClientID: "c", ClientSecret: "s"}})
	if err != nil || m.RepoAccessAvailable() {
		t.Fatalf("no key: available=%v err=%v", m.RepoAccessAvailable(), err)
	}
	if _, err := NewManager(NewMemoryStore(), Config{PublicURL: "http://ui.test", TokenKey: []byte("short"), TokenStore: NewMemoryStore()}); err == nil {
		t.Error("a key that isn't 32 bytes must be refused")
	}
}
