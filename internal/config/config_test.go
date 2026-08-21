package config

import (
	"testing"
	"time"
)

// TestLoad_Defaults verifies that Load() succeeds and produces sensible
// defaults when NO environment variables are set at all.
//
// GO TESTING CONVENTIONS (all enforced by the `go test` tool, not just
// style preference):
//   - The file MUST be named *_test.go — `go build` ignores these files
//     entirely, only `go test` compiles and runs them. This means test
//     code never bloats your production binary.
//   - Test function names MUST start with `Test` and take exactly one
//     parameter: `t *testing.T`. This is how `go test` discovers which
//     functions to run — via reflection over this exact signature, not
//     a config file listing test names.
//   - `t.Errorf` records a failure but lets the test function keep
//     running (useful for checking multiple things and reporting all
//     failures at once). `t.Fatalf` records a failure AND stops the
//     current test function immediately — use this when a later
//     assertion would panic or be meaningless without the earlier one
//     succeeding (e.g. don't try to read fields off a nil pointer).
func TestLoad_Defaults(t *testing.T) {
	// t.Setenv sets an environment variable for the duration of this
	// test only, and automatically restores the previous value when the
	// test finishes — even if it fails. This is Go 1.17+'s built-in
	// replacement for manually calling os.Setenv + defer os.Unsetenv,
	// and critically, it also marks the test as unsafe to run in
	// parallel with other tests that touch the same env var (Go's
	// testing framework handles this automatically).
	//
	// We don't set anything here deliberately — this test's whole point
	// is verifying defaults kick in with a clean environment.

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want %q", cfg.Port, "8080")
	}
	if cfg.AIBaseURL != "http://localhost:8000" {
		t.Errorf("AIBaseURL = %q, want %q", cfg.AIBaseURL, "http://localhost:8000")
	}
	if cfg.K8sTimeout != 10*time.Second {
		t.Errorf("K8sTimeout = %v, want %v", cfg.K8sTimeout, 10*time.Second)
	}
	if cfg.AITimeout != 90*time.Second {
		t.Errorf("AITimeout = %v, want %v", cfg.AITimeout, 90*time.Second)
	}
	if cfg.TroubleshootTimeout != 120*time.Second {
		t.Errorf("TroubleshootTimeout = %v, want %v", cfg.TroubleshootTimeout, 120*time.Second)
	}
}

// TestLoad_Overrides uses TABLE-DRIVEN TESTING — the pattern your
// friend's plan explicitly calls for ("table-driven golden unit tests"
// in Phase 3, and we're establishing the pattern here in Phase 2
// instead of waiting). Rather than writing N separate near-identical
// test functions, we define a table (slice) of input/expected-output
// pairs and loop over it. This is by far the most common testing idiom
// in the Go standard library itself.
func TestLoad_Overrides(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		envVal string
		check  func(t *testing.T, cfg *Config)
	}{
		{
			name:   "custom port",
			envKey: "PORT",
			envVal: "9090",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != "9090" {
					t.Errorf("Port = %q, want %q", cfg.Port, "9090")
				}
			},
		},
		{
			name:   "custom AI base URL",
			envKey: "AI_BASE_URL",
			envVal: "http://ai-service:8000",
			check: func(t *testing.T, cfg *Config) {
				if cfg.AIBaseURL != "http://ai-service:8000" {
					t.Errorf("AIBaseURL = %q, want %q", cfg.AIBaseURL, "http://ai-service:8000")
				}
			},
		},
		{
			name:   "custom K8s timeout",
			envKey: "K8S_TIMEOUT",
			envVal: "5s",
			check: func(t *testing.T, cfg *Config) {
				if cfg.K8sTimeout != 5*time.Second {
					t.Errorf("K8sTimeout = %v, want %v", cfg.K8sTimeout, 5*time.Second)
				}
			},
		},
	}

	for _, tt := range tests {
		// t.Run creates a SUBTEST — this gives each table entry its own
		// named result in `go test -v` output (e.g.
		// "TestLoad_Overrides/custom_port"), and critically, a failure
		// in one subtest doesn't stop the others from running, and you
		// can target a single subtest with `go test -run`.
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envKey, tt.envVal)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() returned unexpected error: %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

// TestLoad_InvalidDuration verifies that a malformed timeout value fails
// loudly (returns an error) instead of silently falling back to a
// default — this is the "fail fast at startup" behavior we documented
// in config.go's comments, and it deserves its own explicit test so a
// future refactor can't accidentally soften that behavior unnoticed.
func TestLoad_InvalidDuration(t *testing.T) {
	t.Setenv("K8S_TIMEOUT", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with invalid K8S_TIMEOUT: expected an error, got nil")
	}
}

func TestParseAgentTokens(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{name: "empty", raw: "", want: map[string]string{}},
		{name: "single", raw: "prod:abc123", want: map[string]string{"prod": "abc123"}},
		{
			name: "multiple",
			raw:  "prod:abc123,staging:def456",
			want: map[string]string{"prod": "abc123", "staging": "def456"},
		},
		{
			name: "skips malformed entries",
			raw:  "prod:abc123,broken-no-colon,staging:def456",
			want: map[string]string{"prod": "abc123", "staging": "def456"},
		},
		{
			name: "handles whitespace around entries",
			raw:  " prod:abc123 , staging:def456 ",
			want: map[string]string{"prod": "abc123", "staging": "def456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAgentTokens(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseAgentTokens(%q) = %v (len %d), want %v (len %d)", tt.raw, got, len(got), tt.want, len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseAgentTokens(%q)[%q] = %q, want %q", tt.raw, k, got[k], v)
				}
			}
		})
	}
}

func TestParseOrigins(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "single", raw: "http://localhost:5173", want: []string{"http://localhost:5173"}},
		{
			name: "multiple",
			raw:  "http://localhost:5173,http://localhost:3000",
			want: []string{"http://localhost:5173", "http://localhost:3000"},
		},
		{
			name: "trims whitespace",
			raw:  " http://localhost:5173 , http://localhost:3000 ",
			want: []string{"http://localhost:5173", "http://localhost:3000"},
		},
		{name: "wildcard passes through literally", raw: "*", want: []string{"*"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOrigins(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseOrigins(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseOrigins(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoad_DefaultCORSOrigins(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := []string{"http://localhost:5173", "http://localhost:3000"}
	if len(cfg.CORSAllowedOrigins) != len(want) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, want)
	}
	for i := range want {
		if cfg.CORSAllowedOrigins[i] != want[i] {
			t.Errorf("CORSAllowedOrigins[%d] = %q, want %q", i, cfg.CORSAllowedOrigins[i], want[i])
		}
	}
}

func TestLoad_RelayEnabled(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "unset defaults to false", raw: "", want: false},
		{name: "explicit true", raw: "true", want: true},
		{name: "explicit false", raw: "false", want: false},
		{name: "anything else is false", raw: "yes", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.raw != "" {
				t.Setenv("ENABLE_RELAY", tt.raw)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.RelayEnabled != tt.want {
				t.Errorf("RelayEnabled = %v, want %v", cfg.RelayEnabled, tt.want)
			}
		})
	}
}

func TestLoad_LocalModeEnabled(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "defaults to true (local dev convenience)", raw: "", want: true},
		{name: "explicit false (public multi-tenant deployment)", raw: "false", want: false},
		{name: "explicit true", raw: "true", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.raw != "" {
				t.Setenv("ENABLE_LOCAL_MODE", tt.raw)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.LocalModeEnabled != tt.want {
				t.Errorf("LocalModeEnabled = %v, want %v", cfg.LocalModeEnabled, tt.want)
			}
		})
	}
}
