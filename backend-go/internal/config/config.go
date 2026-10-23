// Package config centralizes all environment-driven configuration for the
// backend, per the 12-Factor App principle of storing config in the
// environment rather than in code. Every other package receives its
// configuration as explicit parameters (usually via a *Config passed in
// during construction in main.go) rather than reading environment
// variables itself. This is called "dependency injection via
// constructor" — a very simple, no-framework-needed pattern in Go.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every environment-derived setting the backend needs. Note
// that ALL fields are exported (capitalized) — Go's visibility rule is
// simple: identifiers starting with an uppercase letter are visible
// outside their package ("exported"), lowercase ones are package-private.
// These need to be exported because main.go (a different package) needs
// to read them.
type Config struct {
	// Port is the HTTP port the Go server listens on.
	Port string

	// Kubeconfig is the filesystem path to the kubeconfig file used to
	// authenticate to the cluster. Empty string signals "use in-cluster
	// config instead" — relevant once this runs as a Pod inside
	// Kubernetes rather than on your Mac talking to Kind. We'll use this
	// distinction properly in the k8s client factory (next step).
	Kubeconfig string

	// KubeContext lets us pin to a specific context inside the
	// kubeconfig, e.g. "kind-kubeassist-dev" per the plan, so we don't
	// accidentally talk to the wrong cluster if your kubeconfig has
	// multiple contexts merged in (very common once you've used Kind,
	// EKS, GKE, etc. on the same machine).
	KubeContext string

	// AIBaseURL is where the Python FastAPI service lives, e.g.
	// "http://localhost:8000" locally or "http://ai-service:8000"
	// inside docker-compose (compose gives each service a DNS name
	// matching its service name).
	AIBaseURL string

	// AIAuthToken is the shared static token sent to the Python service,
	// per the plan's "Auth between Go↔Python" note.
	AIAuthToken string

	// DatabaseURL is the Postgres connection string for internal/store.
	DatabaseURL string

	// AgentTokens maps clusterID -> shared secret token, for
	// authenticating remote-cluster agents connecting to internal/tunnel.
	// Parsed from a comma-separated KEY:VALUE list, e.g.
	// AGENT_TOKENS="prod-cluster:abc123,staging-cluster:def456". These
	// are PRE-SEEDED tokens known at startup — additional clusters can
	// also register dynamically at runtime via POST /api/clusters (see
	// internal/tunnel.Hub.RegisterCluster), which is the actual
	// self-serve "download and connect" path a real UI would use.
	AgentTokens map[string]string

	// RelayEnabled turns the whole remote-cluster relay feature on,
	// independent of whether any tokens are pre-seeded via AgentTokens.
	// Without this split, a fresh deployment with zero pre-configured
	// clusters could never accept its FIRST dynamic registration, since
	// main.go previously only built the Hub when AgentTokens was
	// non-empty — a chicken-and-egg problem for genuine self-serve
	// onboarding.
	RelayEnabled bool

	// RelayPublicURL is the externally-reachable WebSocket URL agents
	// should dial — see internal/api.Server.RelayPublicURL for how it's
	// used. Defaults to a localhost value suitable for the exact
	// same-machine testing we've been doing against Kind.
	RelayPublicURL string

	// LocalModeEnabled controls whether THIS server's own configured
	// cluster (via Kubeconfig/KubeContext) is exposed through the
	// public API at all — GET /api/pods, /api/namespaces, /api/cluster,
	// and the local (non-remote) troubleshoot routes. Defaults to true
	// for local development convenience (this is exactly what every
	// curl command we've run against Kind has exercised). MUST be set
	// to false for any public multi-tenant deployment where the
	// operator's own cluster should never be reachable by an
	// unauthenticated visitor — in that mode, EVERY customer interacts
	// exclusively through their own registered cluster + agent via
	// /api/remote/{clusterId}/..., and never sees the operator's
	// cluster at all.
	LocalModeEnabled bool

	// CORSAllowedOrigins lists browser origins permitted to call this
	// API cross-origin — see internal/api/cors.go. Parsed from a
	// comma-separated list, e.g.
	// CORS_ALLOWED_ORIGINS="http://localhost:5173,https://app.example.com".
	// Defaults to common local frontend dev-server ports so `go run`
	// works out of the box against a React dev server with zero config
	// — an operator deploying to production should set this explicitly
	// to their real frontend's origin(s) rather than relying on the dev
	// default.
	CORSAllowedOrigins []string

	// AdminToken, when set, is required (as a Bearer token) to list ALL
	// registered clusters via GET /api/clusters. When unset, that listing
	// is only available in local mode — in a public multi-tenant
	// deployment, one customer must not be able to enumerate the others.
	AdminToken string

	// PublicURL is the browser-facing origin of the UI (PUBLIC_URL), e.g.
	// https://app.example.com. OAuth callbacks and post-login redirects
	// are built from it, and an https:// value makes session cookies
	// Secure. Defaults to the Vite dev server.
	PublicURL string

	// GitHubClientID / GitHubClientSecret enable "Sign in with GitHub"
	// (a GitHub OAuth App whose callback URL is
	// PUBLIC_URL/api/auth/github/callback).
	GitHubClientID     string
	GitHubClientSecret string

	// AuthDevLogin enables a password-less "sign in as any username"
	// endpoint for local development and CI (AUTH_DEV_LOGIN=true). It is
	// refused when PUBLIC_URL is https.
	AuthDevLogin bool

	// SessionTTL is how long a browser session lasts.
	SessionTTL time.Duration

	// MaxClustersPerUser caps registrations per account (0 = unlimited).
	MaxClustersPerUser int

	// Rate limits as "N/unit" (e.g. "10/h"; "off" disables): cluster
	// registration per user, troubleshoot per user (or IP for local
	// mode), and sign-in per IP.
	RateLimitRegister     string
	RateLimitTroubleshoot string
	RateLimitLogin        string

	// GitHubTokenKey (GITHUB_TOKEN_KEY, base64 of 32 random bytes)
	// encrypts users' GitHub repository tokens at rest. Set it, together
	// with GitHub sign-in, to enable fix pull requests.
	GitHubTokenKey []byte

	// AlertInterval is how often clusters with alerts enabled are
	// scanned (ALERT_INTERVAL, minimum 15s).
	AlertInterval time.Duration

	// AlertsAllowPrivateWebhooks lets alert webhooks target http:// and
	// private/loopback addresses (ALERTS_ALLOW_PRIVATE_WEBHOOKS=true).
	// Local development only: in production it would let users make the
	// server call internal services.
	AlertsAllowPrivateWebhooks bool

	// TrustProxyHeaders keys anonymous rate limits by X-Forwarded-For.
	// Enable only when a reverse proxy you control sets that header.
	TrustProxyHeaders bool

	// DBConnectTimeout bounds how long startup waits for Postgres to
	// accept connections (compose start ordering, serverless cold starts).
	DBConnectTimeout time.Duration

	// Timeouts — the plan specifies concrete values per external call:
	// Go→K8s 5-10s, Go→AI 60-90s, overall troubleshoot 120s. We make
	// these configurable (with the plan's defaults baked in) rather than
	// hardcoding magic numbers deep in HTTP client code, so they can be
	// tuned per-environment (e.g. slower in CI) without a code change.
	K8sTimeout          time.Duration
	AITimeout           time.Duration
	TroubleshootTimeout time.Duration
}

// Load reads environment variables and returns a validated Config, or an
// error if something required is missing/invalid.
//
// WHY RETURN (*Config, error) INSTEAD OF JUST *Config?
// This is Go's standard idiom for "this operation can fail, and the
// caller MUST be allowed to handle that failure explicitly." Go has no
// exceptions — there is no try/catch. Instead, any function that can
// fail returns an error as its last return value, and the convention is
// the caller checks `if err != nil` immediately after calling it. This
// is a deliberate language design choice: it makes every possible
// failure point visible in the code you're reading, rather than hidden
// in a stack trace that could originate anywhere. It reads more verbosely
// than exceptions, but it trades verbosity for making control flow
// explicit and impossible to silently ignore (well — you CAN ignore an
// error in Go by writing `_ = err`, but doing so is visually obvious and
// `go vet` / linters flag unchecked errors).
func Load() (*Config, error) {
	cfg := &Config{
		Port:             getEnvOrDefault("PORT", "8080"),
		Kubeconfig:       os.Getenv("KUBECONFIG"), // empty is valid — means in-cluster
		KubeContext:      os.Getenv("KUBE_CONTEXT"),
		AIBaseURL:        getEnvOrDefault("AI_BASE_URL", "http://localhost:8000"),
		AIAuthToken:      os.Getenv("AI_AUTH_TOKEN"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		AgentTokens:      parseAgentTokens(os.Getenv("AGENT_TOKENS")),
		RelayEnabled:     getEnvOrDefault("ENABLE_RELAY", "false") == "true",
		RelayPublicURL:   getEnvOrDefault("RELAY_PUBLIC_URL", "ws://localhost:8080/agent/connect"),
		LocalModeEnabled: getEnvOrDefault("ENABLE_LOCAL_MODE", "true") == "true",
		AdminToken:       os.Getenv("ADMIN_TOKEN"),

		PublicURL:             getEnvOrDefault("PUBLIC_URL", "http://localhost:5173"),
		GitHubClientID:        os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret:    os.Getenv("GITHUB_CLIENT_SECRET"),
		AuthDevLogin:          getEnvOrDefault("AUTH_DEV_LOGIN", "false") == "true",
		RateLimitRegister:     getEnvOrDefault("RATE_LIMIT_REGISTER", "10/h"),
		RateLimitTroubleshoot: getEnvOrDefault("RATE_LIMIT_TROUBLESHOOT", "30/m"),
		RateLimitLogin:        getEnvOrDefault("RATE_LIMIT_LOGIN", "30/m"),
		TrustProxyHeaders:     getEnvOrDefault("TRUST_PROXY_HEADERS", "false") == "true",

		AlertsAllowPrivateWebhooks: getEnvOrDefault("ALERTS_ALLOW_PRIVATE_WEBHOOKS", "false") == "true",
		CORSAllowedOrigins: parseOrigins(getEnvOrDefault(
			"CORS_ALLOWED_ORIGINS",
			"http://localhost:5173,http://localhost:3000",
		)),
	}

	var err error
	cfg.K8sTimeout, err = getDurationOrDefault("K8S_TIMEOUT", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("config: invalid K8S_TIMEOUT: %w", err)
	}
	cfg.AITimeout, err = getDurationOrDefault("AI_TIMEOUT", 90*time.Second)
	if err != nil {
		return nil, fmt.Errorf("config: invalid AI_TIMEOUT: %w", err)
	}
	cfg.TroubleshootTimeout, err = getDurationOrDefault("TROUBLESHOOT_TIMEOUT", 120*time.Second)
	if err != nil {
		return nil, fmt.Errorf("config: invalid TROUBLESHOOT_TIMEOUT: %w", err)
	}

	cfg.DBConnectTimeout, err = getDurationOrDefault("DB_CONNECT_TIMEOUT", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("config: invalid DB_CONNECT_TIMEOUT: %w", err)
	}

	if raw := os.Getenv("GITHUB_TOKEN_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("config: GITHUB_TOKEN_KEY must be base64 of exactly 32 bytes (openssl rand -base64 32)")
		}
		cfg.GitHubTokenKey = key
	}

	cfg.AlertInterval, err = getDurationOrDefault("ALERT_INTERVAL", time.Minute)
	if err != nil || cfg.AlertInterval < 15*time.Second {
		return nil, fmt.Errorf("config: ALERT_INTERVAL must be a duration of at least 15s, got %q", os.Getenv("ALERT_INTERVAL"))
	}

	cfg.SessionTTL, err = getDurationOrDefault("SESSION_TTL", 30*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: invalid SESSION_TTL: %w", err)
	}

	cfg.MaxClustersPerUser, err = strconv.Atoi(getEnvOrDefault("MAX_CLUSTERS_PER_USER", "10"))
	if err != nil || cfg.MaxClustersPerUser < 0 {
		return nil, fmt.Errorf("config: invalid MAX_CLUSTERS_PER_USER %q", os.Getenv("MAX_CLUSTERS_PER_USER"))
	}

	// We deliberately do NOT require DatabaseURL or AIAuthToken to be
	// set at this stage. Section 12 of the plan explicitly calls for a
	// "works without LLM" detectors-only mode for graders who don't have
	// an API key — so Config.Load() should succeed even with a sparse
	// environment. Whether a given FEATURE can run is checked at the
	// point of use (e.g. internal/ai refuses to call out if AIBaseURL
	// is unset), not globally at startup. This keeps the server usable
	// in degraded mode instead of refusing to boot entirely.

	return cfg, nil
}

// getEnvOrDefault reads an environment variable, returning a fallback if
// it's unset or empty. This tiny helper avoids repeating the same
// if-empty-then-default pattern for every single string field above.
func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getDurationOrDefault reads an environment variable as a Go duration
// string (e.g. "10s", "2m30s" — Go's time.ParseDuration format) or
// returns the fallback if unset. It returns an error if the variable IS
// set but isn't parseable, since a typo'd timeout should fail loudly at
// startup rather than silently falling back to a default the operator
// didn't intend.
func getDurationOrDefault(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	return d, nil
}

// parseOrigins splits a comma-separated origin list and trims
// whitespace around each entry — deliberately simpler than
// parseAgentTokens since there's no key:value structure here, just a
// flat list. "*" is a valid entry, matched specially by
// internal/api.NewCORSMiddleware to mean "allow any origin."
func parseOrigins(raw string) []string {
	if raw == "" {
		return nil
	}
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

// Malformed entries (missing a colon) are silently skipped rather than
// failing config.Load() entirely — a typo in one cluster's token
// shouldn't prevent the whole backend from starting, since other
// clusters' agents should still be able to connect.
func parseAgentTokens(raw string) map[string]string {
	tokens := make(map[string]string)
	if raw == "" {
		return tokens
	}
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		tokens[parts[0]] = parts[1]
	}
	return tokens
}
