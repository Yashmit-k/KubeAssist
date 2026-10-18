package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/testfixtures"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/tunnel"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func newLocalServer(t *testing.T, objs ...runtime.Object) *Server {
	t.Helper()
	return &Server{
		Collector:           collector.New(fake.NewSimpleClientset(objs...), "kind-test", "v1.31.0"),
		TroubleshootTimeout: 10 * time.Second,
		ClusterName:         "kind-test",
		ServerVersion:       "v1.31.0",
		LocalModeEnabled:    true,
	}
}

func crashObjs() []runtime.Object {
	p, objs := testfixtures.CrashLoopPod("shop", "api-1")
	return append(objs, p)
}

func do(t *testing.T, h http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTroubleshoot_DetectorOnlyWhenNoAI(t *testing.T) {
	s := newLocalServer(t, crashObjs()...)
	rec := do(t, s.NewRouter(), "POST", "/api/pods/shop/api-1/troubleshoot", `{"question":"why?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res model.TroubleshootResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.FailureType != "CrashLoopBackOff" || res.Mode != "detector_only_stub" {
		t.Errorf("got failureType=%q mode=%q", res.FailureType, res.Mode)
	}
	// Playbook commands are filled with the real names.
	found := false
	for _, st := range res.ResolutionSteps {
		if st.Command != nil && strings.Contains(*st.Command, "kubectl logs api-1 -n shop -c api --previous") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a concrete kubectl logs command for the crashing container, got %+v", res.ResolutionSteps)
	}
}

func TestTroubleshoot_FallsBackWhenAIDown(t *testing.T) {
	s := newLocalServer(t, crashObjs()...)
	s.AIClient = ai.New("http://127.0.0.1:1", "token", time.Second) // nothing listens on port 1
	rec := do(t, s.NewRouter(), "POST", "/api/pods/shop/api-1/troubleshoot", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("AI outage should degrade, not fail: status %d: %s", rec.Code, rec.Body)
	}
	var res model.TroubleshootResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Mode != "detector_only_stub" || res.AIError == "" {
		t.Errorf("want detector_only_stub with aiError, got mode=%q aiError=%q", res.Mode, res.AIError)
	}
}

func TestTroubleshoot_UsesAIAndEnforcesFailureType(t *testing.T) {
	var gotSnapshot model.DiagnosticSnapshot
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotSnapshot)
		_ = json.NewEncoder(w).Encode(model.DiagnosisResponse{
			SchemaVersion: "1.1.0", FailureType: "OOMKilled", // wrong on purpose
			IssueSummary: "s", ObservedEvidence: []string{"e"}, ProbableRootCause: "r", Reasoning: "x",
			ConfidenceScore: 0.8, ResolutionSteps: []model.ResolutionStep{{Order: 1, Description: "d"}},
			Mode: "full_rag",
		})
	}))
	defer aiSrv.Close()

	s := newLocalServer(t, crashObjs()...)
	s.AIClient = ai.New(aiSrv.URL, "secret", 5*time.Second)
	rec := do(t, s.NewRouter(), "POST", "/api/pods/shop/api-1/troubleshoot", `{"question":"my password=hunter2 leaked?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var res model.TroubleshootResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.FailureType != "CrashLoopBackOff" {
		t.Errorf("Go must override a reclassifying AI: got %q", res.FailureType)
	}
	if res.VerificationCommands == nil || res.UncertaintyNotes == nil || res.DocumentationReferences == nil {
		t.Error("missing arrays must be normalized to []")
	}
	if strings.Contains(gotSnapshot.UserQuestion, "hunter2") {
		t.Error("user question must be redacted before leaving Go")
	}
	if _, ok := gotSnapshot.Pod.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok {
		t.Error("last-applied-configuration must be stripped before leaving Go")
	}
}

func TestTroubleshoot_PodNotFound(t *testing.T) {
	s := newLocalServer(t)
	rec := do(t, s.NewRouter(), "POST", "/api/pods/shop/nope/troubleshoot", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body)
	}
}

func TestTroubleshootStream_EmitsStagesThenDone(t *testing.T) {
	s := newLocalServer(t, crashObjs()...)
	srv := httptest.NewServer(s.NewRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/pods/shop/api-1/troubleshoot/stream?question=hi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}

	var stages []string
	var doneData string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if event == "done" {
				doneData = data
			} else if event == "" {
				var st struct{ Stage string }
				_ = json.Unmarshal([]byte(data), &st)
				stages = append(stages, st.Stage)
			}
		case line == "":
			event = ""
		}
	}
	if len(stages) < 3 || stages[0] != stageCollecting || stages[1] != stageDetecting {
		t.Errorf("unexpected stages %v", stages)
	}
	var res model.TroubleshootResult
	if err := json.Unmarshal([]byte(doneData), &res); err != nil || res.FailureType != "CrashLoopBackOff" {
		t.Errorf("bad done payload %q (%v)", doneData, err)
	}
}

func TestPodListAndDetail(t *testing.T) {
	s := newLocalServer(t, append(crashObjs(), testfixtures.HealthyPod("shop", "ok-1"))...)
	h := s.NewRouter()

	rec := do(t, h, "GET", "/api/pods?namespace=shop", "")
	var pods []model.PodSummary
	_ = json.Unmarshal(rec.Body.Bytes(), &pods)
	if len(pods) != 2 || pods[0].Status != "CrashLoopBackOff" {
		t.Errorf("pods = %+v", pods)
	}

	rec = do(t, h, "GET", "/api/pods/shop/api-1", "")
	var snap model.DiagnosticSnapshot
	_ = json.Unmarshal(rec.Body.Bytes(), &snap)
	if snap.Detector.FailureType != "CrashLoopBackOff" {
		t.Errorf("pod detail should include detector result, got %+v", snap.Detector)
	}
}

func TestLocalModeDisabled_RoutesAbsent(t *testing.T) {
	s := &Server{Hub: tunnel.NewHub(nil), TroubleshootTimeout: time.Second}
	h := s.NewRouter()
	for _, path := range []string{"/api/pods", "/api/namespaces", "/api/investigations", "/api/cluster"} {
		if rec := do(t, h, "GET", path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s should not exist with local mode off, got %d", path, rec.Code)
		}
	}
	if rec := do(t, h, "GET", "/api/clusters", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("cluster listing needs a session, got %d", rec.Code)
	}
}

func TestHealth_ReportsFeatures(t *testing.T) {
	s := newLocalServer(t)
	rec := do(t, s.NewRouter(), "GET", "/api/health", "")
	var h healthResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	if h.Status != "ok" || !h.LocalMode || h.AI != "disabled" || h.Store != "disabled" {
		t.Errorf("health = %+v", h)
	}
}

// The detector-only answer swaps the playbook's generic write step for
// the evidence-derived one, and keeps the read-only investigation steps.
func TestStubDiagnosis_UsesConcreteFix(t *testing.T) {
	snap := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{Name: "oom-1", Namespace: "shop", Phase: "Running"},
		Detector: model.DetectorResult{FailureType: "OOMKilled", Confidence: 0.9, Evidence: []string{"x"}, SearchTerms: []string{},
			Fix: &model.FixPlan{Steps: []model.FixStep{{Description: "Raise to 64Mi", Command: "kubectl set resources deployment/oom -n shop -c app --limits=memory=64Mi", IsWriteAction: true}}}},
	}
	d := stubDiagnosis(snap, "")
	var cmds []string
	for _, st := range d.ResolutionSteps {
		if st.Command != nil {
			cmds = append(cmds, *st.Command)
		}
	}
	all := strings.Join(cmds, "\n")
	if strings.Contains(all, "<new-limit>") || !strings.Contains(all, "--limits=memory=64Mi") || !strings.Contains(all, "kubectl describe pod oom-1 -n shop") {
		t.Errorf("steps = %s", all)
	}
	last := d.ResolutionSteps[len(d.ResolutionSteps)-1]
	if !last.IsWriteAction || last.Order != len(d.ResolutionSteps) {
		t.Errorf("fix should be the last, write-flagged step: %+v", last)
	}
}

// Re-registering a cluster issues a new token; re-applying the manifest
// must change the pod template so the running agent restarts with it.
func TestAgentManifest_NewTokenChangesPodTemplate(t *testing.T) {
	a := agentManifest("prod", "token-one", "wss://relay/agent/connect", "img")
	b := agentManifest("prod", "token-two", "wss://relay/agent/connect", "img")
	annotation := func(m string) string {
		for _, line := range strings.Split(m, "\n") {
			if strings.Contains(line, "kubeassist.io/token-hash:") {
				return line
			}
		}
		return ""
	}
	if annotation(a) == "" || annotation(a) == annotation(b) {
		t.Fatalf("token-hash annotation missing or unchanged: %q vs %q", annotation(a), annotation(b))
	}
	if strings.Contains(annotation(a), "token-one") {
		t.Error("annotation must hold a hash, not the token")
	}
	if agentManifest("prod", "token-one", "wss://relay/agent/connect", "img") != a {
		t.Error("manifest must be deterministic for the same token (no needless restarts)")
	}
}
