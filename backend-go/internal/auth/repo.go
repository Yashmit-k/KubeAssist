package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/github"
)

// TokenStore keeps each user's sealed GitHub repository-access token.
type TokenStore interface {
	SaveGitHubToken(ctx context.Context, userID string, sealed []byte, scopes string) error
	GitHubToken(ctx context.Context, userID string) (sealed []byte, scopes string, err error) // nil when none
	DeleteGitHubToken(ctx context.Context, userID string) error
}

// ErrNoRepoAccess means the user hasn't connected GitHub repositories.
var ErrNoRepoAccess = errors.New("GitHub repository access is not connected")

// Repository access is a SEPARATE authorization from sign-in: signing in
// asks GitHub for nothing but the public profile, and only users who
// want fix pull requests grant write access, scoped to public
// repositories unless they opt into private ones.
const (
	scopePublic  = "public_repo"
	scopePrivate = "repo"
)

// RepoAccessAvailable reports whether fix PRs can be offered at all.
func (m *Manager) RepoAccessAvailable() bool {
	return m.github != nil && m.aead != nil && m.tokens != nil
}

// GitHubAPI is the API root tokens are valid for.
func (m *Manager) GitHubAPI() string { return m.githubAPI }

// HTTPClient is the client used for GitHub calls.
func (m *Manager) HTTPClient() *http.Client { return m.httpClient }

// GitHubToken returns the user's repository token and its scopes.
func (m *Manager) GitHubToken(ctx context.Context, userID string) (string, string, error) {
	if !m.RepoAccessAvailable() {
		return "", "", ErrNoRepoAccess
	}
	sealed, scopes, err := m.tokens.GitHubToken(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if sealed == nil {
		return "", "", ErrNoRepoAccess
	}
	tok, err := m.open(sealed, userID)
	if err != nil {
		return "", "", fmt.Errorf("auth: stored GitHub token can't be decrypted (was GITHUB_TOKEN_KEY changed?): %w", err)
	}
	return tok, scopes, nil
}

// StoreGitHubToken seals and saves a user's repository token.
func (m *Manager) StoreGitHubToken(ctx context.Context, userID, token, scopes string) error {
	if !m.RepoAccessAvailable() {
		return ErrNoRepoAccess
	}
	sealed, err := m.seal(token, userID)
	if err != nil {
		return err
	}
	return m.tokens.SaveGitHubToken(ctx, userID, sealed, scopes)
}

// seal encrypts a token with AES-GCM, bound to the user ID so a row
// copied to another user doesn't decrypt.
func (m *Manager) seal(token, userID string) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return m.aead.Seal(nonce, nonce, []byte(token), []byte(userID)), nil
}

func (m *Manager) open(sealed []byte, userID string) (string, error) {
	n := m.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed token too short")
	}
	plain, err := m.aead.Open(nil, sealed[:n], sealed[n:], []byte(userID))
	return string(plain), err
}

func (m *Manager) repoCookieName() string {
	if m.secure {
		return "__Host-kubeassist_oauth_repo"
	}
	return "kubeassist_oauth_repo"
}

func (m *Manager) repoOAuth(private bool) *oauth2.Config {
	c := *m.github
	c.RedirectURL = m.publicURL + "/api/auth/github/callback/repo"
	c.Scopes = []string{scopePublic}
	if private {
		c.Scopes = []string{scopePrivate}
	}
	return &c
}

// handleRepoAccessStatus: GET /api/auth/github/repo-access.
func (m *Manager) handleRepoAccessStatus(w http.ResponseWriter, r *http.Request) {
	u, err := m.CurrentUser(r)
	if err != nil || u == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "not signed in")
		return
	}
	resp := map[string]any{"available": true, "connected": false, "githubAccount": u.Provider == ProviderGitHub}
	if _, scopes, err := m.GitHubToken(r.Context(), u.ID); err == nil {
		resp["connected"], resp["scopes"] = true, scopes
		resp["private"] = hasScope(scopes, scopePrivate)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRepoAccessStart: GET /api/auth/github/repo-access/start?private=1&next=/...
func (m *Manager) handleRepoAccessStart(w http.ResponseWriter, r *http.Request) {
	next := SafeNext(r.URL.Query().Get("next"))
	u, err := m.CurrentUser(r)
	if err != nil || u == nil {
		http.Redirect(w, r, m.publicURL+"/connect?authError=login_required", http.StatusFound)
		return
	}
	if u.Provider != ProviderGitHub {
		http.Redirect(w, r, m.publicURL+withParam(next, "repoAccessError", "github_account_required"), http.StatusFound)
		return
	}
	private := r.URL.Query().Get("private") == "1"
	state, verifier := randomHex(16), oauth2.GenerateVerifier()
	priv := "0"
	if private {
		priv = "1"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.repoCookieName(),
		Value:    state + "." + verifier + "." + priv + "." + base64.RawURLEncoding.EncodeToString([]byte(next)),
		Path:     "/",
		MaxAge:   int(oauthCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, m.repoOAuth(private).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// handleRepoAccessCallback finishes the repository authorization. The
// GitHub account that approved must be the one the user signed in with.
func (m *Manager) handleRepoAccessCallback(w http.ResponseWriter, r *http.Request) {
	next := "/"
	fail := func(code string) {
		m.clearCookie(w, m.repoCookieName())
		http.Redirect(w, r, m.publicURL+withParam(next, "repoAccessError", code), http.StatusFound)
	}
	c, err := r.Cookie(m.repoCookieName())
	parts := []string{}
	if err == nil {
		parts = strings.SplitN(c.Value, ".", 4)
	}
	if len(parts) == 4 {
		if b, err := base64.RawURLEncoding.DecodeString(parts[3]); err == nil {
			next = SafeNext(string(b))
		}
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(e)
		return
	}
	if len(parts) != 4 || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(q.Get("state"))) != 1 {
		fail("invalid_state")
		return
	}
	u, err := m.CurrentUser(r)
	if err != nil || u == nil {
		fail("login_required")
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, m.httpClient)
	tok, err := m.repoOAuth(parts[2] == "1").Exchange(ctx, q.Get("code"), oauth2.VerifierOption(parts[1]))
	if err != nil {
		log.Printf("auth: repo-access code exchange failed: %v", err)
		fail("exchange_failed")
		return
	}
	gh, err := github.New(m.githubAPI, tok.AccessToken, m.httpClient).User(ctx)
	if err != nil {
		fail("profile_failed")
		return
	}
	if u.Provider != ProviderGitHub || strconv.FormatInt(gh.ID, 10) != u.ProviderUserID {
		fail("account_mismatch")
		return
	}
	if err := m.StoreGitHubToken(r.Context(), u.ID, tok.AccessToken, gh.Scopes); err != nil {
		log.Printf("auth: saving repo token: %v", err)
		fail("save_failed")
		return
	}
	m.clearCookie(w, m.repoCookieName())
	http.Redirect(w, r, m.publicURL+withParam(next, "repoAccess", "connected"), http.StatusFound)
}

// handleRepoAccessDisconnect deletes the stored token and revokes it on
// GitHub (best effort).
func (m *Manager) handleRepoAccessDisconnect(w http.ResponseWriter, r *http.Request) {
	u, err := m.CurrentUser(r)
	if errors.Is(err, ErrUntrustedOrigin) {
		writeError(w, http.StatusForbidden, "untrusted_origin", err.Error())
		return
	}
	if err != nil || u == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "not signed in")
		return
	}
	if tok, _, err := m.GitHubToken(r.Context(), u.ID); err == nil {
		m.revokeToken(r.Context(), tok)
	}
	if err := m.tokens.DeleteGitHubToken(r.Context(), u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "disconnect_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeToken: DELETE /applications/{client_id}/token (OAuth app basic auth).
func (m *Manager) revokeToken(ctx context.Context, token string) {
	body, _ := json.Marshal(map[string]string{"access_token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, m.githubAPI+"/applications/"+url.PathEscape(m.github.ClientID)+"/token", strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.SetBasicAuth(m.github.ClientID, m.github.ClientSecret)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.Printf("auth: revoking GitHub token: %v", err)
		return
	}
	resp.Body.Close()
}

func hasScope(scopes, want string) bool {
	for _, s := range strings.Split(scopes, ",") {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// withParam appends a query parameter to a same-site path.
func withParam(path, key, value string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}
