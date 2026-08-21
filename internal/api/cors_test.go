package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := NewCORSMiddleware([]string{"http://localhost:5173"})(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d — request should still reach the inner handler", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := NewCORSMiddleware([]string{"http://localhost:5173"})(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty — disallowed origin should get no CORS header", got)
	}
	// IMPORTANT: the request still reaches the inner handler and gets a
	// normal 200. This is intentional and correct — CORS is enforced by
	// the BROWSER refusing to let JavaScript read the response, not by
	// the server refusing to process the request. A server-side 403
	// here would be a misunderstanding of what CORS actually protects
	// against (it's not an access-control mechanism for the server; the
	// real access control is RBAC/auth, already handled elsewhere).
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d — CORS doesn't block server-side processing, only browser-side reading", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_NoOriginHeader(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := NewCORSMiddleware([]string{"http://localhost:5173"})(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil) // no Origin header — like curl
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for a non-browser request with no Origin header", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCORSMiddleware_PreflightShortCircuits(t *testing.T) {
	innerWasCalled := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerWasCalled = true
		w.WriteHeader(http.StatusOK)
	})
	handler := NewCORSMiddleware([]string{"http://localhost:5173"})(inner)

	req := httptest.NewRequest(http.MethodOptions, "/api/pods/default/some-pod/troubleshoot", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if innerWasCalled {
		t.Error("preflight OPTIONS request should NOT reach the inner handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d for a preflight response", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("preflight response missing Access-Control-Allow-Methods header")
	}
}

func TestCORSMiddleware_WildcardAllowsAnyOrigin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := NewCORSMiddleware([]string{"*"})(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://any-random-origin.example.com")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://any-random-origin.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the echoed origin under wildcard config", got)
	}
}
