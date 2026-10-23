// Package auth implements KubeAssist accounts: sign-in through an
// identity provider (GitHub OAuth, or a development-only login), and
// browser sessions carried in an HttpOnly, SameSite=Lax cookie.
//
// Sessions are opaque random tokens. Only their SHA-256 hash is stored
// (Postgres via internal/store, or MemoryStore without a database), so a
// database dump does not hand out working sessions. Agents never use
// sessions — they keep authenticating with their per-cluster token.
package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// Store persists users and sessions. internal/store implements it
// against Postgres; MemoryStore is the no-database fallback.
type Store interface {
	// UpsertUser creates the user identified by (Provider,
	// ProviderUserID) or refreshes its profile fields, returning it with
	// its ID set.
	UpsertUser(ctx context.Context, u model.User) (model.User, error)
	CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error
	// SessionUser returns the user for a live session, or (nil, nil) if
	// the session doesn't exist or has expired.
	SessionUser(ctx context.Context, tokenHash string) (*model.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
}

// Provider names, as reported by Manager.Providers and stored on users.
const (
	ProviderGitHub = "github"
	ProviderDev    = "dev"
)

const (
	defaultSessionTTL = 30 * 24 * time.Hour
	oauthCookieTTL    = 10 * time.Minute
	maxNextLen        = 512
)

// GitHubConfig enables "Sign in with GitHub". Endpoint and APIBaseURL
// default to github.com and only change in tests (or for GitHub
// Enterprise).
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	Endpoint     oauth2.Endpoint
	APIBaseURL   string
}

// Config configures a Manager.
type Config struct {
	// PublicURL is the browser-facing origin of the UI, e.g.
	// https://app.example.com. OAuth callbacks are built from it
	// (PublicURL + /api/auth/github/callback — the UI's proxy forwards
	// /api to the backend), post-login redirects land on it, and an
	// https:// value turns on Secure cookies.
	PublicURL string

	// TrustedOrigins are extra browser origins (beyond PublicURL and the
	// request's own host) allowed to make state-changing requests with a
	// session cookie — normally CORS_ALLOWED_ORIGINS.
	TrustedOrigins []string

	SessionTTL time.Duration // default 30 days
	GitHub     *GitHubConfig // nil disables GitHub sign-in

	// DevLogin enables POST /api/auth/dev-login, which signs anyone in
	// as any username without a password. Development and CI only.
	DevLogin bool

	// HTTPClient is used for calls to the identity provider.
	HTTPClient *http.Client

	// TokenKey (32 bytes) encrypts GitHub repository-access tokens at
	// rest; with TokenStore it enables "Connect GitHub repositories"
	// (fix pull requests). Nil disables the feature.
	TokenKey   []byte
	TokenStore TokenStore
}

// Manager issues and resolves sessions and serves the /api/auth routes.
type Manager struct {
	store          Store
	cfg            Config
	publicURL      string
	secure         bool
	cookieName     string
	trustedOrigins map[string]bool
	github         *oauth2.Config
	githubAPI      string
	httpClient     *http.Client
	tokens         TokenStore
	aead           cipher.AEAD // seals repository tokens; nil when disabled
}

// NewManager validates cfg and builds a Manager.
func NewManager(store Store, cfg Config) (*Manager, error) {
	pu, err := url.Parse(cfg.PublicURL)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return nil, fmt.Errorf("auth: PUBLIC_URL must be an absolute http(s) URL, got %q", cfg.PublicURL)
	}
	secure := pu.Scheme == "https"
	if cfg.DevLogin && secure {
		return nil, errors.New("auth: AUTH_DEV_LOGIN must not be enabled on an https:// PUBLIC_URL (it lets anyone sign in as anyone)")
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}

	m := &Manager{
		store:          store,
		cfg:            cfg,
		publicURL:      strings.TrimRight(cfg.PublicURL, "/"),
		secure:         secure,
		cookieName:     "kubeassist_session",
		trustedOrigins: map[string]bool{pu.Scheme + "://" + pu.Host: true},
		httpClient:     cfg.HTTPClient,
	}
	// The __Host- prefix makes browsers refuse the cookie unless it is
	// Secure, host-only and Path=/ — so a sibling subdomain can't plant
	// or overwrite it. It requires https, hence only when secure.
	if secure {
		m.cookieName = "__Host-kubeassist_session"
	}
	for _, o := range cfg.TrustedOrigins {
		if o != "" && o != "*" {
			m.trustedOrigins[strings.TrimRight(o, "/")] = true
		}
	}
	if gh := cfg.GitHub; gh != nil {
		if gh.ClientID == "" || gh.ClientSecret == "" {
			return nil, errors.New("auth: GitHub sign-in needs both GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET")
		}
		endpoint := gh.Endpoint
		if endpoint.AuthURL == "" {
			endpoint = oauth2.Endpoint{
				AuthURL:  "https://github.com/login/oauth/authorize",
				TokenURL: "https://github.com/login/oauth/access_token",
			}
		}
		// No scopes: the public profile (id, login, name, avatar) is all
		// we need, and asking for nothing keeps the consent screen honest.
		m.github = &oauth2.Config{
			ClientID:     gh.ClientID,
			ClientSecret: gh.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  m.publicURL + "/api/auth/github/callback",
		}
		m.githubAPI = strings.TrimRight(gh.APIBaseURL, "/")
		if m.githubAPI == "" {
			m.githubAPI = "https://api.github.com"
		}
	}
	if len(cfg.TokenKey) > 0 && cfg.TokenStore != nil {
		if len(cfg.TokenKey) != 32 {
			return nil, errors.New("auth: GITHUB_TOKEN_KEY must decode to exactly 32 bytes (openssl rand -base64 32)")
		}
		block, err := aes.NewCipher(cfg.TokenKey)
		if err != nil {
			return nil, err
		}
		if m.aead, err = cipher.NewGCM(block); err != nil {
			return nil, err
		}
		m.tokens = cfg.TokenStore
	}
	return m, nil
}

// Providers lists the enabled sign-in methods, for /api/health and the UI.
func (m *Manager) Providers() []string {
	p := []string{}
	if m.github != nil {
		p = append(p, ProviderGitHub)
	}
	if m.cfg.DevLogin {
		p = append(p, ProviderDev)
	}
	return p
}

// ErrUntrustedOrigin is returned for a state-changing request carrying a
// session cookie from a browser origin we don't serve (CSRF defense in
// depth on top of SameSite=Lax).
var ErrUntrustedOrigin = errors.New("request origin is not trusted")

// CurrentUser resolves the request's session cookie. It returns (nil,
// nil) when there is no valid session.
func (m *Manager) CurrentUser(r *http.Request) (*model.User, error) {
	c, err := r.Cookie(m.cookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	if !safeMethod(r.Method) && !m.TrustedOrigin(r) {
		return nil, ErrUntrustedOrigin
	}
	return m.store.SessionUser(r.Context(), hashToken(c.Value))
}

// TrustedOrigin reports whether the request's Origin header (if any) is
// the request's own host, PublicURL, or a configured trusted origin.
// Requests without an Origin (curl, same-origin GETs) pass.
func (m *Manager) TrustedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if m.trustedOrigins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && u.Host == r.Host
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// startSession creates a session for u and sets its cookie.
func (m *Manager) startSession(w http.ResponseWriter, r *http.Request, u model.User) error {
	token := randomHex(32)
	if err := m.store.CreateSession(r.Context(), hashToken(token), u.ID, time.Now().Add(m.cfg.SessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(m.cfg.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (m *Manager) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
	})
}

// --- HTTP routes ---

// Limiter decorates a handler with rate limiting; nil means none.
type Limiter func(http.HandlerFunc) http.HandlerFunc

// RegisterRoutes adds the /api/auth routes. limit wraps the sign-in
// endpoints (they create users and sessions).
func (m *Manager) RegisterRoutes(mux *http.ServeMux, limit Limiter) {
	if limit == nil {
		limit = func(h http.HandlerFunc) http.HandlerFunc { return h }
	}
	mux.HandleFunc("GET /api/auth/me", m.handleMe)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	if m.github != nil {
		mux.HandleFunc("GET /api/auth/github/login", limit(m.handleGitHubLogin))
		mux.HandleFunc("GET /api/auth/github/callback", limit(m.handleGitHubCallback))
	}
	if m.cfg.DevLogin {
		mux.HandleFunc("POST /api/auth/dev-login", limit(m.handleDevLogin))
	}
	if m.RepoAccessAvailable() {
		mux.HandleFunc("GET /api/auth/github/repo-access", m.handleRepoAccessStatus)
		mux.HandleFunc("GET /api/auth/github/repo-access/start", limit(m.handleRepoAccessStart))
		mux.HandleFunc("GET /api/auth/github/callback/repo", limit(m.handleRepoAccessCallback))
		mux.HandleFunc("DELETE /api/auth/github/repo-access", m.handleRepoAccessDisconnect)
	}
}

func (m *Manager) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := m.CurrentUser(r)
	switch {
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "could not check your session")
	case u == nil:
		writeError(w, http.StatusUnauthorized, "unauthenticated", "not signed in")
	default:
		writeJSON(w, http.StatusOK, u)
	}
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !m.TrustedOrigin(r) {
		writeError(w, http.StatusForbidden, "untrusted_origin", ErrUntrustedOrigin.Error())
		return
	}
	if c, err := r.Cookie(m.cookieName); err == nil && c.Value != "" {
		if err := m.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			log.Printf("auth: deleting session: %v", err)
		}
	}
	m.clearCookie(w, m.cookieName)
	w.WriteHeader(http.StatusNoContent)
}

var devLoginPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,37}[a-z0-9])?$`)

func (m *Manager) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !m.TrustedOrigin(r) {
		writeError(w, http.StatusForbidden, "untrusted_origin", ErrUntrustedOrigin.Error())
		return
	}
	var req struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	login := strings.ToLower(strings.TrimSpace(req.Login))
	if !devLoginPattern.MatchString(login) {
		writeError(w, http.StatusBadRequest, "invalid_login", "login must be 1-39 lowercase letters, digits or '-'")
		return
	}
	u, err := m.store.UpsertUser(r.Context(), model.User{Provider: ProviderDev, ProviderUserID: login, Login: login, Name: login})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "login_failed", err.Error())
		return
	}
	if err := m.startSession(w, r, u); err != nil {
		writeError(w, http.StatusInternalServerError, "login_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// SafeNext returns next if it is a same-site relative path, else "/".
// It stops the post-login redirect being used as an open redirect.
func SafeNext(next string) string {
	if next == "" || len(next) > maxNextLen || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	return next
}

// --- helpers ---

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// --- MemoryStore ---

// MemoryStore keeps users and sessions in process memory. It backs the
// no-database development setup; everything is lost on restart.
type MemoryStore struct {
	mu       sync.Mutex
	users    map[string]model.User // keyed by ID
	byIdent  map[string]string     // provider + "\x00" + providerUserID -> ID
	sessions map[string]memSession
	tokens   map[string]memToken
}

type memToken struct {
	sealed []byte
	scopes string
}

type memSession struct {
	userID    string
	expiresAt time.Time
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: map[string]model.User{}, byIdent: map[string]string{}, sessions: map[string]memSession{}, tokens: map[string]memToken{}}
}

func (s *MemoryStore) UpsertUser(_ context.Context, u model.User) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := u.Provider + "\x00" + u.ProviderUserID
	if id, ok := s.byIdent[key]; ok {
		u.ID = id
	} else {
		u.ID = newUUID()
		s.byIdent[key] = u.ID
	}
	s.users[u.ID] = u
	return u, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, tokenHash, userID string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.sessions {
		if now.After(v.expiresAt) {
			delete(s.sessions, k)
		}
	}
	s.sessions[tokenHash] = memSession{userID: userID, expiresAt: expiresAt}
	return nil
}

func (s *MemoryStore) SessionUser(_ context.Context, tokenHash string) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tokenHash]
	if !ok || time.Now().After(sess.expiresAt) {
		return nil, nil
	}
	u, ok := s.users[sess.userID]
	if !ok {
		return nil, nil
	}
	return &u, nil
}

func (s *MemoryStore) DeleteSession(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

func (s *MemoryStore) SaveGitHubToken(_ context.Context, userID string, sealed []byte, scopes string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[userID] = memToken{sealed: append([]byte{}, sealed...), scopes: scopes}
	return nil
}

func (s *MemoryStore) GitHubToken(_ context.Context, userID string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[userID]
	if !ok {
		return nil, "", nil
	}
	return t.sealed, t.scopes, nil
}

func (s *MemoryStore) DeleteGitHubToken(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, userID)
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("auth: crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
