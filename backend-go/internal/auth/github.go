package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// The OAuth round trip keeps its state (CSRF), PKCE verifier and the
// post-login path in a short-lived HttpOnly cookie, so the backend
// needs no server-side storage for half-finished logins.
func (m *Manager) oauthCookieName() string {
	if m.secure {
		return "__Host-kubeassist_oauth"
	}
	return "kubeassist_oauth"
}

// handleGitHubLogin starts "Sign in with GitHub": GET
// /api/auth/github/login?next=/connect redirects the browser to GitHub.
func (m *Manager) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	state := randomHex(16)
	verifier := oauth2.GenerateVerifier()
	next := SafeNext(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name:     m.oauthCookieName(),
		Value:    state + "." + verifier + "." + base64.RawURLEncoding.EncodeToString([]byte(next)),
		Path:     "/",
		MaxAge:   int(oauthCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, m.github.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// handleGitHubCallback finishes the OAuth flow: verify state, exchange
// the code (with the PKCE verifier), read the GitHub profile, upsert the
// user, start a session and send the browser back into the app.
func (m *Manager) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(code string) {
		m.clearCookie(w, m.oauthCookieName())
		http.Redirect(w, r, m.publicURL+"/connect?authError="+url.QueryEscape(code), http.StatusFound)
	}

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(e) // e.g. access_denied when the user cancels
		return
	}
	c, err := r.Cookie(m.oauthCookieName())
	if err != nil {
		fail("login_expired")
		return
	}
	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(q.Get("state"))) != 1 {
		fail("invalid_state")
		return
	}
	verifier := parts[1]
	nextBytes, _ := base64.RawURLEncoding.DecodeString(parts[2])
	next := SafeNext(string(nextBytes))

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, m.httpClient)
	tok, err := m.github.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		log.Printf("auth: github code exchange failed: %v", err)
		fail("exchange_failed")
		return
	}
	profile, err := m.githubProfile(ctx, tok.AccessToken)
	if err != nil {
		log.Printf("auth: reading github profile failed: %v", err)
		fail("profile_failed")
		return
	}
	u, err := m.store.UpsertUser(r.Context(), profile)
	if err != nil {
		log.Printf("auth: saving user: %v", err)
		fail("login_failed")
		return
	}
	if err := m.startSession(w, r, u); err != nil {
		log.Printf("auth: creating session: %v", err)
		fail("login_failed")
		return
	}
	m.clearCookie(w, m.oauthCookieName())
	http.Redirect(w, r, m.publicURL+next, http.StatusFound)
}

func (m *Manager) githubProfile(ctx context.Context, accessToken string) (model.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.githubAPI+"/user", nil)
	if err != nil {
		return model.User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return model.User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return model.User{}, fmt.Errorf("github /user returned %d: %s", resp.StatusCode, body)
	}
	var p struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p); err != nil {
		return model.User{}, err
	}
	if p.ID == 0 || p.Login == "" {
		return model.User{}, fmt.Errorf("github /user response is missing id or login")
	}
	// Keyed by GitHub's numeric ID, not the login: logins can be renamed
	// and later claimed by someone else.
	return model.User{
		Provider:       ProviderGitHub,
		ProviderUserID: strconv.FormatInt(p.ID, 10),
		Login:          p.Login,
		Name:           p.Name,
		AvatarURL:      p.AvatarURL,
	}, nil
}
