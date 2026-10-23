package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/testfixtures"
)

// fakeChatAI scripts the AI service: reply(step) decides each step's
// response, and every request is recorded.
func fakeChatAI(t *testing.T, reply func(step int, req ai.ChatRequest) ai.ChatResponse) (*ai.Client, *[]ai.ChatRequest) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []ai.ChatRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/chat" || r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req ai.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen = append(seen, req)
		step := len(seen) - 1
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(reply(step, req))
	}))
	t.Cleanup(srv.Close)
	return ai.New(srv.URL, "secret", 5*time.Second), &seen
}

func chatBody(q string) string {
	b, _ := json.Marshal(chatRequest{Messages: []ai.ChatMessage{{Role: "user", Content: q}}})
	return string(b)
}

func TestChat_RunsToolsAndReturnsTheAnswer(t *testing.T) {
	crash, objs := testfixtures.CrashLoopPod("shop", "api-7d9f")
	s := newLocalServer(t, append(objs, crash, testfixtures.HealthyPod("shop", "web-1"))...)
	client, seen := fakeChatAI(t, func(step int, req ai.ChatRequest) ai.ChatResponse {
		if step == 0 {
			return ai.ChatResponse{Type: "tool_calls", ToolCalls: []ai.ChatToolCall{
				{ID: "c1", Name: "diagnose_pod", Arguments: map[string]any{"name": "api"}}, // partial name
				{ID: "c2", Name: "list_pods", Arguments: map[string]any{"namespace": "shop"}},
			}}
		}
		return ai.ChatResponse{Type: "answer", Content: "api-7d9f is crash-looping.", Mode: "rules"}
	})
	s.AIClient = client

	rec := do(t, s.NewRouter(), "POST", "/api/chat", chatBody("why is api failing?"))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	var out chatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Reply != "api-7d9f is crash-looping." || out.Mode != "rules" || len(out.Tools) != 2 {
		t.Fatalf("response = %+v", out)
	}
	if out.Tools[0].Summary != "Diagnosed shop/api-7d9f: CrashLoopBackOff" || !out.Tools[0].OK {
		t.Errorf("tool summary = %+v", out.Tools[0])
	}

	// The second step carried both executed results back to the AI.
	second := (*seen)[1]
	if len(second.ToolResults) != 2 || second.ToolResults[0].ID != "c1" {
		t.Fatalf("tool results = %+v", second.ToolResults)
	}
	diag, _ := json.Marshal(second.ToolResults[0].Result)
	if !strings.Contains(string(diag), `"failureType":"CrashLoopBackOff"`) || !strings.Contains(string(diag), `"fix":`) {
		t.Errorf("diagnose result = %s", diag)
	}
	pods, _ := json.Marshal(second.ToolResults[1].Result)
	if !strings.Contains(string(pods), `"total":2`) || !strings.Contains(string(pods), `"unhealthy":1`) {
		t.Errorf("list_pods result = %s", pods)
	}
}

func TestChat_ForcesAnAnswerAfterMaxRounds(t *testing.T) {
	s := newLocalServer(t, testfixtures.HealthyPod("shop", "web-1"))
	client, seen := fakeChatAI(t, func(step int, req ai.ChatRequest) ai.ChatResponse {
		if req.ForceAnswer {
			return ai.ChatResponse{Type: "answer", Content: "done", Mode: "llm"}
		}
		return ai.ChatResponse{Type: "tool_calls", ToolCalls: []ai.ChatToolCall{{ID: "x", Name: "list_namespaces"}}}
	})
	s.AIClient = client
	rec := do(t, s.NewRouter(), "POST", "/api/chat", chatBody("loop forever"))
	var out chatResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Reply != "done" || len(*seen) != maxChatRounds+1 || len(out.Tools) != maxChatRounds {
		t.Errorf("code=%d out=%+v steps=%d", rec.Code, out, len(*seen))
	}
}

func TestChat_PodResolutionAndBadArguments(t *testing.T) {
	a := testfixtures.HealthyPod("shop", "web-1")
	b := testfixtures.HealthyPod("shop", "web-2")
	s := newLocalServer(t, a, b)
	results := map[string]any{}
	client, _ := fakeChatAI(t, func(step int, req ai.ChatRequest) ai.ChatResponse {
		switch step {
		case 0:
			return ai.ChatResponse{Type: "tool_calls", ToolCalls: []ai.ChatToolCall{
				{ID: "amb", Name: "diagnose_pod", Arguments: map[string]any{"name": "web"}},
				{ID: "none", Name: "get_logs", Arguments: map[string]any{"name": "nope"}},
				{ID: "bad", Name: "list_pods", Arguments: map[string]any{"namespace": "Not Valid!"}},
			}}
		}
		for _, r := range req.ToolResults {
			results[r.ID] = r.Result
		}
		return ai.ChatResponse{Type: "answer", Content: "ok"}
	})
	s.AIClient = client
	if rec := do(t, s.NewRouter(), "POST", "/api/chat", chatBody("why is web failing")); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	amb, _ := json.Marshal(results["amb"])
	if !strings.Contains(string(amb), `Several pods match`) || !strings.Contains(string(amb), `"shop/web-1"`) {
		t.Errorf("ambiguous = %s", amb)
	}
	none, _ := json.Marshal(results["none"])
	if !strings.Contains(string(none), `No pod matching \"nope\"`) {
		t.Errorf("missing = %s", none)
	}
	bad, _ := json.Marshal(results["bad"])
	if !strings.Contains(string(bad), "not a valid namespace") {
		t.Errorf("bad namespace = %s", bad)
	}
}

func TestChat_Validation(t *testing.T) {
	s := newLocalServer(t)
	if rec := do(t, s.NewRouter(), "POST", "/api/chat", chatBody("hi")); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no AI service: want 503, got %d", rec.Code)
	}
	s.AIClient, _ = fakeChatAI(t, func(int, ai.ChatRequest) ai.ChatResponse { return ai.ChatResponse{Type: "answer", Content: "x"} })
	h := s.NewRouter()
	for _, bad := range []string{`{}`, `{"messages":[{"role":"system","content":"x"}]}`,
		`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`,
		`{"messages":[{"role":"user","content":"` + strings.Repeat("a", maxChatContent+1) + `"}]}`} {
		if rec := do(t, h, "POST", "/api/chat", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%.60s: want 400, got %d", bad, rec.Code)
		}
	}
}

// Logs for an init-container failure come from (and are labelled as) the
// failing init container, not the app container that never started.
func TestChat_LogsNameTheFailingInitContainer(t *testing.T) {
	s := newLocalServer(t, testfixtures.InitFailurePod("shop", "init-1"))
	var got any
	s.AIClient, _ = fakeChatAI(t, func(step int, req ai.ChatRequest) ai.ChatResponse {
		if step == 0 {
			return ai.ChatResponse{Type: "tool_calls", ToolCalls: []ai.ChatToolCall{{ID: "l", Name: "get_logs", Arguments: map[string]any{"name": "init-1"}}}}
		}
		got = req.ToolResults[0].Result
		return ai.ChatResponse{Type: "answer", Content: "ok"}
	})
	if rec := do(t, s.NewRouter(), "POST", "/api/chat", chatBody("logs for init-1")); rec.Code != http.StatusOK {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	b, _ := json.Marshal(got)
	init := testfixtures.InitFailurePod("shop", "x").Spec.InitContainers[0].Name
	if !strings.Contains(string(b), `"container":"`+init+`"`) {
		t.Errorf("logs result = %s (want container %q)", b, init)
	}
}
