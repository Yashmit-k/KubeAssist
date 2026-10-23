package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/ai"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

const (
	chatTimeout       = 120 * time.Second
	maxChatRounds     = 4 // tool rounds per user message before forcing an answer
	maxToolsPerRound  = 3
	maxChatMessages   = 20
	maxChatContent    = 2000
	maxChatLogLines   = 60
	maxChatListedPods = 60
)

type chatRequest struct {
	Messages []ai.ChatMessage `json:"messages"`
}

// chatToolUse tells the UI what was looked at to produce the answer.
type chatToolUse struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Summary   string         `json:"summary"`
	OK        bool           `json:"ok"`
}

type chatResponse struct {
	Reply string        `json:"reply"`
	Mode  string        `json:"mode"` // "llm" | "rules"
	Tools []chatToolUse `json:"tools"`
}

// handleChat answers a question about the cluster. The AI service
// decides which read-only tools to use (an LLM, or a rule-based planner
// without one); this loop runs them against the cluster — the only place
// that can — and feeds the results back until there is an answer.
// Nothing here can change the cluster: every tool is a read.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	if s.AIClient == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_unavailable", "chat needs the AI service (set AI_BASE_URL and AI_AUTH_TOKEN)")
		return
	}
	var req chatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}
	if msg := validateChat(req.Messages); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_chat", msg)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), chatTimeout)
	defer cancel()
	step := ai.ChatRequest{Messages: req.Messages}
	used := []chatToolUse{}
	for round := 0; ; round++ {
		step.ForceAnswer = round >= maxChatRounds
		resp, err := s.AIClient.Chat(ctx, step)
		if err != nil {
			log.Printf("api: chat step failed: %v", err)
			writeError(w, http.StatusBadGateway, "ai_error", "the AI service could not answer: "+truncate(err.Error(), 200))
			return
		}
		if resp.Type == "answer" || len(resp.ToolCalls) == 0 || step.ForceAnswer {
			reply := strings.TrimSpace(resp.Content)
			if reply == "" {
				reply = "I couldn't put an answer together from what I found. Try asking about a specific pod or namespace."
			}
			writeJSON(w, http.StatusOK, chatResponse{Reply: reply, Mode: nonEmptyStr(resp.Mode, "rules"), Tools: used})
			return
		}
		calls := resp.ToolCalls
		if len(calls) > maxToolsPerRound {
			calls = calls[:maxToolsPerRound]
		}
		for _, call := range calls {
			result, summary, ok := s.runChatTool(ctx, src, call)
			step.ToolResults = append(step.ToolResults, ai.ChatToolResult{ChatToolCall: call, Result: result})
			used = append(used, chatToolUse{Name: call.Name, Arguments: call.Arguments, Summary: summary, OK: ok})
		}
	}
}

func validateChat(msgs []ai.ChatMessage) string {
	if len(msgs) == 0 || len(msgs) > maxChatMessages {
		return fmt.Sprintf("send 1-%d messages", maxChatMessages)
	}
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			return "message roles must be user or assistant"
		}
		if strings.TrimSpace(m.Content) == "" || len(m.Content) > maxChatContent {
			return fmt.Sprintf("messages must be 1-%d characters", maxChatContent)
		}
	}
	if msgs[len(msgs)-1].Role != "user" {
		return "the last message must be from the user"
	}
	return ""
}

// --- tools (all read-only) ---

// k8sName bounds tool arguments to valid Kubernetes names.
var k8sName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

type toolError struct {
	Error      string   `json:"error"`
	Candidates []string `json:"candidates,omitempty"`
}

func strArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(strings.ToLower(v))
}

// runChatTool executes one tool and returns its result (sent to the AI),
// a one-line summary (shown in the UI), and whether it succeeded.
func (s *Server) runChatTool(ctx context.Context, src clusterSource, call ai.ChatToolCall) (any, string, bool) {
	ns := strArg(call.Arguments, "namespace")
	if ns != "" && !k8sName.MatchString(ns) {
		return toolError{Error: fmt.Sprintf("%q is not a valid namespace name.", ns)}, "Invalid namespace", false
	}
	where := "all namespaces"
	if ns != "" {
		where = ns
	}

	switch call.Name {
	case "list_namespaces":
		names, err := src.ListNamespaces(ctx)
		if err != nil {
			return toolError{Error: "Could not list namespaces: " + sourceErrText(err)}, "Couldn't list namespaces", false
		}
		return map[string]any{"namespaces": names}, fmt.Sprintf("Listed %d namespaces", len(names)), true

	case "list_pods":
		pods, err := src.ListPods(ctx, ns)
		if err != nil {
			return toolError{Error: "Could not list pods: " + sourceErrText(err)}, "Couldn't list pods", false
		}
		unhealthy := 0
		out := make([]scanPod, 0, len(pods))
		for _, p := range pods {
			if needsScan(p) {
				unhealthy++
			}
			if len(out) < maxChatListedPods {
				out = append(out, scanPod{p.Namespace, p.Name, p.Status, p.Ready, p.RestartCount})
			}
		}
		return map[string]any{"namespace": ns, "total": len(pods), "unhealthy": unhealthy, "pods": out},
			fmt.Sprintf("Listed %d pods in %s (%d need attention)", len(pods), where, unhealthy), true

	case "scan":
		res, err := runScan(ctx, src, ns)
		if err != nil {
			return toolError{Error: "Scan failed: " + sourceErrText(err)}, "Scan failed", false
		}
		broken := 0
		for _, g := range res.Groups {
			broken += len(g.Pods)
		}
		return res, fmt.Sprintf("Scanned %s: %d unhealthy pods, %d root causes", where, broken, len(res.Groups)), true

	case "diagnose_pod", "get_logs":
		podNS, pod, cands, err := resolvePod(ctx, src, ns, strArg(call.Arguments, "name"))
		if err != nil {
			return toolError{Error: err.Error(), Candidates: cands}, "Couldn't find the pod", false
		}
		snap, err := src.CollectCore(ctx, podNS, pod)
		if err != nil {
			return toolError{Error: fmt.Sprintf("Could not read pod %s/%s: %s", podNS, pod, sourceErrText(err))}, "Couldn't read the pod", false
		}
		if call.Name == "get_logs" {
			return s.chatLogs(ctx, src, snap, call.Arguments)
		}
		return chatDiagnose(ctx, src, snap)
	}
	return toolError{Error: fmt.Sprintf("Unknown tool %q.", call.Name)}, "Unknown tool", false
}

func chatDiagnose(ctx context.Context, src clusterSource, snap *model.DiagnosticSnapshot) (any, string, bool) {
	core := detect.Detect(snap)
	snap.Detector = core
	if needsRelatedCheck(core, nil) {
		if related, err := src.CollectRelated(ctx, snap.Pod.Namespace, snap.Pod.Name, nil); err == nil {
			snap.Related = related
			snap.Detector = detect.DetectRelated(snap, core)
		}
	}
	detect.Enrich(snap)
	redact.Snapshot(snap)
	d := snap.Detector
	_, cause := detect.RootCause(snap)
	if d.FailureType == "Healthy" {
		cause = ""
	}
	st := podState(snap)
	return map[string]any{
		"namespace": snap.Pod.Namespace, "name": snap.Pod.Name, "failureType": d.FailureType, "rootCause": cause,
		"evidence": firstN(d.Evidence, 5), "fix": d.Fix, "status": st.Status, "ready": st.Ready, "restarts": st.RestartCount,
	}, fmt.Sprintf("Diagnosed %s/%s: %s", snap.Pod.Namespace, snap.Pod.Name, d.FailureType), true
}

func (s *Server) chatLogs(ctx context.Context, src clusterSource, snap *model.DiagnosticSnapshot, args map[string]any) (any, string, bool) {
	previous, _ := args["previous"].(bool)
	container := strArg(args, "container")
	// The snapshot's logs are the failing container's (e.g. an init
	// container); classify first so FocusContainer names that one.
	snap.Detector = detect.Detect(snap)
	focus := detect.FocusContainer(snap)
	var text string
	switch {
	case container != "" && container != focus:
		logs, err := src.FetchContainerLogs(ctx, snap.Pod.Namespace, snap.Pod.Name, container, previous)
		if err != nil {
			return toolError{Error: fmt.Sprintf("Could not read logs of container %q: %s", container, sourceErrText(err))}, "Couldn't read logs", false
		}
		text = redact.Text(logs)
	case previous && snap.Logs.Previous != nil:
		container, text = focus, redact.Text(*snap.Logs.Previous)
	default:
		container, text = focus, redact.Text(snap.Logs.Current)
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > maxChatLogLines {
		lines = lines[len(lines)-maxChatLogLines:]
	}
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	out := map[string]any{"namespace": snap.Pod.Namespace, "name": snap.Pod.Name, "container": container,
		"previous": previous, "logs": strings.Join(lines, "\n"), "lines": len(lines)}
	if len(lines) == 0 && snap.Logs.UnavailableReason != "" {
		out["unavailableReason"] = snap.Logs.UnavailableReason
	}
	return out, fmt.Sprintf("Read %d log lines of %s/%s (%s)", len(lines), snap.Pod.Namespace, snap.Pod.Name, container), true
}

// resolvePod turns "oom-failure" into the actual pod: an exact name,
// else the one pod whose name contains it (preferring prefixes, which is
// how Deployment pods are named). Ambiguity is returned as candidates.
func resolvePod(ctx context.Context, src clusterSource, ns, name string) (string, string, []string, error) {
	if name == "" || !k8sName.MatchString(name) {
		return "", "", nil, fmt.Errorf("%q is not a valid pod name", name)
	}
	pods, err := src.ListPods(ctx, ns)
	if err != nil {
		return "", "", nil, fmt.Errorf("could not list pods: %s", sourceErrText(err))
	}
	var exact, prefix, contains []model.PodSummary
	for _, p := range pods {
		switch {
		case p.Name == name:
			exact = append(exact, p)
		case strings.HasPrefix(p.Name, name):
			prefix = append(prefix, p)
		case strings.Contains(p.Name, name):
			contains = append(contains, p)
		}
	}
	for _, set := range [][]model.PodSummary{exact, prefix, contains} {
		switch len(set) {
		case 0:
			continue
		case 1:
			return set[0].Namespace, set[0].Name, nil, nil
		default:
			cands := make([]string, 0, len(set))
			for _, p := range set {
				cands = append(cands, p.Namespace+"/"+p.Name)
			}
			return "", "", cands, fmt.Errorf("Several pods match %q.", name)
		}
	}
	where := "any namespace"
	if ns != "" {
		where = "namespace " + ns
	}
	return "", "", nil, fmt.Errorf("No pod matching %q in %s.", name, where)
}

func sourceErrText(err error) string {
	return truncate(err.Error(), 160)
}
