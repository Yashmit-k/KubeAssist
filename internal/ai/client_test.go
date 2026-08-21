package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yourname/kubeassist/backend-go/internal/model"
)

// TestTroubleshoot_Success spins up a REAL local HTTP server
// (httptest.NewServer) that behaves like a correctly-functioning Python
// service, and verifies our client parses its response correctly.
//
// httptest.NewServer starts an actual TCP listener on localhost with a
// random free port and serves real HTTP over it — this is a genuine
// integration test of our HTTP client code, not a mock of net/http
// itself. It's fast and hermetic (no external network needed) because
// it's entirely local.
func TestTroubleshoot_Success(t *testing.T) {
	want := model.DiagnosisResponse{
		IssueSummary:      "Container is crash-looping due to OOM",
		ProbableRootCause: "Memory limit too low for workload",
		ConfidenceScore:   0.9,
		FailureType:       "OOMKilled",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/troubleshoot" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing/incorrect auth header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	client := New(server.URL, "test-token", 5*time.Second)
	got, err := client.Troubleshoot(context.Background(), &model.DiagnosticSnapshot{})
	if err != nil {
		t.Fatalf("Troubleshoot() unexpected error: %v", err)
	}
	if got.FailureType != want.FailureType {
		t.Errorf("FailureType = %q, want %q", got.FailureType, want.FailureType)
	}
	if got.ConfidenceScore != want.ConfidenceScore {
		t.Errorf("ConfidenceScore = %v, want %v", got.ConfidenceScore, want.ConfidenceScore)
	}
}

// TestTroubleshoot_RetriesOn503 verifies the client retries exactly once
// on a 5xx response and succeeds on the second attempt.
func TestTroubleshoot_RetriesOn503(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(model.DiagnosisResponse{FailureType: "Healthy"})
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	client.baseBackoff = 10 * time.Millisecond // speed up the test — don't actually wait 500ms

	got, err := client.Troubleshoot(context.Background(), &model.DiagnosticSnapshot{})
	if err != nil {
		t.Fatalf("Troubleshoot() unexpected error after retry: %v", err)
	}
	if got.FailureType != "Healthy" {
		t.Errorf("FailureType = %q, want %q", got.FailureType, "Healthy")
	}
	if callCount != 2 {
		t.Errorf("expected exactly 2 calls (1 failure + 1 retry), got %d", callCount)
	}
}

// TestTroubleshoot_DoesNotRetryOn400 verifies a client error (bad
// request) fails IMMEDIATELY without burning a retry, per the plan's
// "No retry storm" rule — retrying an identical malformed payload can
// never succeed.
func TestTroubleshoot_DoesNotRetryOn400(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid snapshot"}`))
	}))
	defer server.Close()

	client := New(server.URL, "", 5*time.Second)
	client.baseBackoff = 10 * time.Millisecond

	_, err := client.Troubleshoot(context.Background(), &model.DiagnosticSnapshot{})
	if err == nil {
		t.Fatal("expected an error for 400 response, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 call (no retry on 400), got %d", callCount)
	}
}

// TestTroubleshoot_ContextCancellation verifies that cancelling the
// caller's context stops the request rather than letting it run to
// completion.
func TestTroubleshoot_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // simulate a slow/hung Python service
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(server.URL, "", 10*time.Second) // client timeout is long...

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond) // ...but caller wants out fast
	defer cancel()

	start := time.Now()
	_, err := client.Troubleshoot(ctx, &model.DiagnosticSnapshot{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if elapsed > 1*time.Second {
		t.Errorf("request took %v, expected it to abort quickly after context deadline (~50ms)", elapsed)
	}
}
