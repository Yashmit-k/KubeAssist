package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/alerts"
)

func TestAlerts_ConfigLifecycleAndOwnership(t *testing.T) {
	var (
		mu  sync.Mutex
		got []string
	)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body["event"].(string))
		mu.Unlock()
	}))
	defer hook.Close()

	s := newRelayServer(t)
	s.Alerts = alerts.NewWatcher(alerts.NewMemoryStore(), nil, alerts.NewSender(true), time.Minute, func(id string) string { return "http://ui.test/c/" + id + "/scan" })
	s.AlertInterval = time.Minute
	h := s.NewRouter()
	alice := login(t, h, "alice")
	bob := login(t, h, "bob")
	register(t, h, alice, "prod")
	path := "/api/remote/prod/alerts"

	rec := do(t, h, "GET", path, "", "Cookie", alice)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("empty config: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", path, "", "Cookie", bob); rec.Code != http.StatusNotFound {
		t.Errorf("another user must not see alerts: %d", rec.Code)
	}
	if rec := do(t, h, "PUT", path, `{"kind":"webhook","url":"`+hook.URL+`","enabled":true}`, "Cookie", bob); rec.Code != http.StatusNotFound {
		t.Errorf("another user must not set alerts: %d", rec.Code)
	}

	for body, code := range map[string]string{
		`{"kind":"sms","url":"https://x.test/h","enabled":true}`: "invalid_kind",
		`{"kind":"slack","enabled":true}`:                        "missing_url",
		`{"kind":"slack","url":"ftp://x.test/h","enabled":true}`: "invalid_url",
	} {
		rec := do(t, h, "PUT", path, body, "Cookie", alice)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s: want 400 %s, got %d %s", body, code, rec.Code, rec.Body)
		}
	}

	rec = do(t, h, "PUT", path, `{"kind":"webhook","url":"`+hook.URL+`/T0/B0/SECRETxyz9","enabled":true}`, "Cookie", alice)
	var view alertConfigView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if rec.Code != http.StatusOK || !view.Configured || !view.Enabled || strings.Contains(rec.Body.String(), "SECRET") || !strings.HasSuffix(view.URL, "…xyz9") {
		t.Fatalf("saved view must mask the URL: %d %s", rec.Code, rec.Body)
	}

	// Saving without a URL keeps the stored one (the UI only has the mask).
	rec = do(t, h, "PUT", path, `{"kind":"webhook","enabled":false}`, "Cookie", alice)
	cfg, _ := s.Alerts.Store().GetAlertConfig(t.Context(), "prod")
	if rec.Code != http.StatusOK || cfg == nil || cfg.Enabled || !strings.HasSuffix(cfg.URL, "/T0/B0/SECRETxyz9") {
		t.Fatalf("toggle without URL: %d %+v", rec.Code, cfg)
	}

	rec = do(t, h, "POST", path+"/test", "", "Cookie", alice)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test send: %d %s", rec.Code, rec.Body)
	}
	mu.Lock()
	if len(got) != 1 || got[0] != "test" {
		t.Errorf("receiver got %v", got)
	}
	mu.Unlock()

	// Revoking the cluster removes its alerts too.
	if rec := do(t, h, "DELETE", "/api/clusters/prod", "", "Cookie", alice); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if c, _ := s.Alerts.Store().GetAlertConfig(t.Context(), "prod"); c != nil {
		t.Errorf("alert config must go with the cluster: %+v", c)
	}
}

func TestAlerts_ProductionSenderRefusesInternalURLs(t *testing.T) {
	s := newRelayServer(t)
	s.Alerts = alerts.NewWatcher(alerts.NewMemoryStore(), nil, alerts.NewSender(false), time.Minute, nil)
	h := s.NewRouter()
	alice := login(t, h, "alice")
	register(t, h, alice, "prod")
	for _, u := range []string{"http://hooks.example.com/x", "https://127.0.0.1/x", "https://169.254.169.254/latest"} {
		rec := do(t, h, "PUT", "/api/remote/prod/alerts", `{"kind":"webhook","url":"`+u+`","enabled":true}`, "Cookie", alice)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", u, rec.Code)
		}
	}
}

func TestAlerts_RoutesAbsentWithoutWatcher(t *testing.T) {
	s := newRelayServer(t)
	h := s.NewRouter()
	alice := login(t, h, "alice")
	register(t, h, alice, "prod")
	if rec := do(t, h, "GET", "/api/remote/prod/alerts", "", "Cookie", alice); rec.Code != http.StatusNotFound {
		t.Errorf("no watcher: want 404, got %d", rec.Code)
	}
}
