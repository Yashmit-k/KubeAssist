package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func cause(key string) Cause {
	return Cause{Key: key, Title: "Problem " + key, FailureType: "CrashLoopBackOff", Pods: []string{"lab/" + key + "-1"}, Fix: "kubectl edit deployment/" + key}
}

// harness: a scripted scan and a recording webhook receiver.
type harness struct {
	mu       sync.Mutex
	next     []Cause
	err      error
	received []map[string]any
	w        *Watcher
	cfg      Config
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.received = append(h.received, body)
		h.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	scan := func(context.Context, string) ([]Cause, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.next, h.err
	}
	// allowPrivate: the test receiver is on 127.0.0.1 over http.
	h.w = NewWatcher(NewMemoryStore(), scan, NewSender(true), time.Minute, func(id string) string { return "https://ui.test/c/" + id + "/scan" })
	h.cfg = Config{ClusterID: "prod", Kind: KindWebhook, URL: srv.URL, Enabled: true}
	return h
}

func (h *harness) scanWith(causes ...Cause) []map[string]any {
	h.mu.Lock()
	h.next, h.err = causes, nil
	before := len(h.received)
	h.mu.Unlock()
	h.w.Check(context.Background(), h.cfg)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.received[before:]
}

func keys(list any) []string {
	var out []string
	for _, c := range list.([]any) {
		out = append(out, c.(map[string]any)["key"].(string))
	}
	return out
}

func TestWatcher_BaselineDebounceAndResolve(t *testing.T) {
	h := newHarness(t)

	// First scan: existing problems are tracked, not alerted.
	if got := h.scanWith(cause("old")); len(got) != 0 {
		t.Fatalf("baseline must not alert, got %v", got)
	}
	st := h.w.Status("prod")
	if !st.Baseline || len(st.Active) != 1 || st.LastScanAt == nil {
		t.Fatalf("status after baseline = %+v", st)
	}

	// A new problem must be seen twice before it alerts.
	if got := h.scanWith(cause("old"), cause("new")); len(got) != 0 {
		t.Fatalf("one sighting must not alert, got %v", got)
	}
	got := h.scanWith(cause("old"), cause("new"))
	if len(got) != 1 || got[0]["event"] != "change" || strings.Join(keys(got[0]["new"]), ",") != "new" || len(got[0]["resolved"].([]any)) != 0 {
		t.Fatalf("second sighting should alert exactly 'new', got %v", got)
	}
	if got[0]["url"] != "https://ui.test/c/prod/scan" || got[0]["cluster"] != "prod" {
		t.Errorf("payload = %v", got[0])
	}

	// A flapping problem (seen once, then gone) never alerts.
	h.scanWith(cause("old"), cause("new"), cause("flap"))
	if got := h.scanWith(cause("old"), cause("new")); len(got) != 0 {
		t.Fatalf("flapping cause must not alert, got %v", got)
	}

	// Resolution also needs two scans without the problem.
	if got := h.scanWith(cause("new")); len(got) != 0 {
		t.Fatalf("one miss must not resolve, got %v", got)
	}
	got = h.scanWith(cause("new"))
	if len(got) != 1 || strings.Join(keys(got[0]["resolved"]), ",") != "old" || len(got[0]["new"].([]any)) != 0 {
		t.Fatalf("want 'old' resolved, got %v", got)
	}
	if st := h.w.Status("prod"); len(st.Active) != 1 || st.Active[0].Key != "new" || st.LastNotifiedAt == nil || st.LastDeliveryError != "" {
		t.Errorf("status = %+v", st)
	}
}

func TestWatcher_UnavailableAndReset(t *testing.T) {
	h := newHarness(t)
	h.scanWith(cause("a"))
	h.mu.Lock()
	h.err = ErrUnavailable
	h.mu.Unlock()
	h.w.Check(context.Background(), h.cfg)
	if st := h.w.Status("prod"); st.LastScanError != "agent not connected" || len(st.Active) != 1 {
		t.Errorf("an offline agent must not resolve anything: %+v", st)
	}
	h.w.Reset("prod")
	if st := h.w.Status("prod"); st.Baseline || len(st.Active) != 0 {
		t.Errorf("reset should forget state: %+v", st)
	}
	if got := h.scanWith(cause("b")); len(got) != 0 {
		t.Errorf("after reset the next scan is a fresh baseline, got %v", got)
	}
}

func TestWatcher_CheckAllSkipsDisabled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.w.store.PutAlertConfig(ctx, Config{ClusterID: "off", Kind: KindWebhook, URL: h.cfg.URL, Enabled: false})
	h.w.CheckAll(ctx)
	if st := h.w.Status("off"); st.LastScanAt != nil {
		t.Errorf("disabled config must not be scanned: %+v", st)
	}
}

func TestSender_BlocksInternalAddresses(t *testing.T) {
	s := NewSender(false)
	for _, bad := range []string{
		"http://hooks.example.com/x",          // not https
		"https://127.0.0.1/x",                 // loopback
		"https://169.254.169.254/latest/meta", // cloud metadata
		"https://10.0.0.5/hook",               // private
		"https://[::1]/x",                     // v6 loopback
		"https://user:pw@hooks.example.com/x", // credentials
		"not a url",
	} {
		if err := s.ValidateURL(bad); err == nil {
			t.Errorf("ValidateURL(%q) should fail", bad)
		}
	}
	if err := s.ValidateURL("https://hooks.slack.com/services/T0/B0/xyz"); err != nil {
		t.Errorf("public https URL rejected: %v", err)
	}

	// A hostname that resolves to loopback passes the URL check but is
	// refused at connect time.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must never reach an internal address")
	}))
	defer srv.Close()
	u := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	if err := s.Send(context.Background(), KindWebhook, u+"/hook", Notice{Cluster: "c", Test: true}); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("want ErrBlockedAddress, got %v", err)
	}
}

func TestFormat(t *testing.T) {
	n := Notice{Cluster: "prod", New: []Cause{cause("a")}, Resolved: []Cause{cause("b")}, Link: "https://ui.test/c/prod/scan"}
	slack, _ := Format(KindSlack, n)
	var s map[string]string
	_ = json.Unmarshal(slack, &s)
	if !strings.Contains(s["text"], "🔴 *1 new problem in `prod`*") || !strings.Contains(s["text"], "Fix: `kubectl edit deployment/a`") ||
		!strings.Contains(s["text"], "✅ *Resolved in `prod`*") || !strings.Contains(s["text"], "<https://ui.test/c/prod/scan|Open the health scan>") {
		t.Errorf("slack = %s", s["text"])
	}
	many := Notice{Cluster: "prod"}
	for i := 0; i < 40; i++ {
		c := cause("x")
		c.Title = strings.Repeat("long title ", 10)
		many.New = append(many.New, c)
	}
	discord, _ := Format(KindDiscord, many)
	var d map[string]string
	_ = json.Unmarshal(discord, &d)
	if len(d["content"]) > 2000 || !strings.Contains(d["content"], "…and 35 more") {
		t.Errorf("discord content length %d", len(d["content"]))
	}
	test, _ := Format(KindDiscord, Notice{Cluster: "prod", Test: true})
	if !strings.Contains(string(test), "test alert") {
		t.Errorf("test = %s", test)
	}
	if _, err := Format("sms", n); err == nil {
		t.Error("unknown kind must fail")
	}
}

func TestSender_ReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "invalid_token")
	}))
	defer srv.Close()
	err := NewSender(true).Send(context.Background(), KindSlack, srv.URL, Notice{Cluster: "c", Test: true})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403: invalid_token") {
		t.Errorf("err = %v", err)
	}
}

func TestMaskURL(t *testing.T) {
	if got := MaskURL("https://hooks.slack.com/services/T000/B000/SecretToken1234"); got != "https://hooks.slack.com/…1234" {
		t.Errorf("mask = %q", got)
	}
}
