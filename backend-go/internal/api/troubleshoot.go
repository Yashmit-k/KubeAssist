package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

const maxQuestionLen = 1000

// Pipeline stage names, emitted over SSE in this order. "related" only
// appears when pass-2 collection runs.
const (
	stageCollecting = "collecting"
	stageDetecting  = "detecting"
	stageRelated    = "related"
	stageDiagnosing = "diagnosing"
	stageSaving     = "saving"
)

// stageFunc reports pipeline progress; detail is a short human-readable
// note for the UI (e.g. the detector's classification).
type stageFunc func(stage, detail string)

// pipelineError carries an HTTP status/code alongside the error so the
// POST and SSE handlers can report failures identically.
type pipelineError struct {
	status int
	code   string
	err    error
}

func (e *pipelineError) Error() string { return e.err.Error() }

// runTroubleshoot is THE troubleshoot pipeline, shared by the POST and
// SSE handlers for both local and remote clusters:
//
//	collect (pass 1) -> detect -> [collect related (pass 2) -> re-detect]
//	-> redact -> ONE call to the AI service -> persist
//
// There is no multi-pass protocol with Python (schema 1.1.0 has no
// needsRecollect): every "gather more evidence" decision happens here,
// before the single AI request.
func (s *Server) runTroubleshoot(ctx context.Context, src clusterSource, clusterKey, ns, name string, req model.TroubleshootRequest, emit stageFunc) (*model.TroubleshootResult, error) {
	emit(stageCollecting, fmt.Sprintf("Collecting pod, events, owners and logs for %s/%s", ns, name))
	snapshot, err := src.CollectCore(ctx, ns, name)
	if err != nil {
		if isNotFound(err) {
			return nil, &pipelineError{http.StatusNotFound, "pod_not_found", err}
		}
		return nil, &pipelineError{http.StatusBadGateway, "collection_failed", err}
	}
	if clusterKey != "" && snapshot.Cluster.Name == "" {
		snapshot.Cluster.Name = clusterKey
	}
	snapshot.ServiceName = req.ServiceName

	core := detect.Detect(snapshot)
	snapshot.Detector = core
	emit(stageDetecting, fmt.Sprintf("Detector classified this as %s", core.FailureType))

	if needsRelatedCheck(core, req.ServiceName) {
		emit(stageRelated, "Checking related Services and ConfigMap keys")
		related, err := src.CollectRelated(ctx, ns, name, req.ServiceName)
		if err != nil {
			// Pass 2 is an enrichment: if it fails we still diagnose with
			// pass-1 evidence, and say so.
			log.Printf("api: related-info collection for %s/%s failed: %v", ns, name, err)
			snapshot.Detector.RelatedHints = append(snapshot.Detector.RelatedHints,
				"related Services/ConfigMaps could not be collected: "+truncate(err.Error(), 200))
		} else {
			snapshot.Related = related
			snapshot.Detector = detect.DetectRelated(snapshot, core)
			if snapshot.Detector.FailureType != core.FailureType {
				emit(stageDetecting, fmt.Sprintf("Related resources refined this to %s", snapshot.Detector.FailureType))
			}
		}
	}

	snapshot.UserQuestion = req.Question
	redact.Snapshot(snapshot)

	result := &model.TroubleshootResult{Detector: snapshot.Detector}
	if s.AIClient == nil {
		emit(stageDiagnosing, "AI service not configured — using detector-only diagnosis")
		d := stubDiagnosis(snapshot, "")
		result.DiagnosisResponse = &d
	} else {
		emit(stageDiagnosing, "Retrieving Kubernetes docs and generating a diagnosis")
		diagnosis, err := s.AIClient.Troubleshoot(ctx, snapshot)
		if err != nil {
			if ctx.Err() != nil {
				return nil, &pipelineError{http.StatusGatewayTimeout, "timeout", fmt.Errorf("troubleshoot timed out: %w", ctx.Err())}
			}
			// Degrade instead of failing: the detector's classification
			// and evidence are still correct and useful on their own.
			log.Printf("api: AI service error for %s/%s: %v", ns, name, err)
			d := stubDiagnosis(snapshot, err.Error())
			result.DiagnosisResponse = &d
			result.AIError = truncate(err.Error(), 300)
		} else {
			// The AI must echo Go's classification; enforce it here too
			// rather than trusting the other side of the wire.
			diagnosis.FailureType = snapshot.Detector.FailureType
			normalizeDiagnosis(diagnosis)
			result.DiagnosisResponse = diagnosis
		}
	}

	if s.Store != nil {
		emit(stageSaving, "Saving investigation")
		// Persist on a context detached from the request: a client that
		// disconnects right at the end shouldn't lose the record.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		id, err := s.Store.Save(saveCtx, clusterKey, ns, name, *snapshot, *result.DiagnosisResponse)
		if err != nil {
			log.Printf("api: failed to save investigation: %v", err)
		} else {
			result.InvestigationID = id
		}
	}
	return result, nil
}

// parseTroubleshootRequest reads the optional {question, serviceName}
// from a JSON body (POST) or query string (GET, since EventSource can't
// send a body).
func parseTroubleshootRequest(r *http.Request) (model.TroubleshootRequest, error) {
	var req model.TroubleshootRequest
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			return req, err
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return req, fmt.Errorf("request body must be JSON like {\"question\": \"...\", \"serviceName\": \"...\"}")
			}
		}
	} else {
		q := r.URL.Query()
		req.Question = q.Get("question")
		if svc := q.Get("serviceName"); svc != "" {
			req.ServiceName = &svc
		}
	}
	req.Question = strings.TrimSpace(req.Question)
	if len(req.Question) > maxQuestionLen {
		req.Question = req.Question[:maxQuestionLen]
	}
	if req.ServiceName != nil {
		trimmed := strings.TrimSpace(*req.ServiceName)
		if trimmed == "" {
			req.ServiceName = nil
		} else {
			req.ServiceName = &trimmed
		}
	}
	return req, nil
}

func (s *Server) handleTroubleshoot(w http.ResponseWriter, r *http.Request, src clusterSource, clusterKey string) {
	req, err := parseTroubleshootRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.TroubleshootTimeout)
	defer cancel()

	result, err := s.runTroubleshoot(ctx, src, clusterKey, r.PathValue("ns"), r.PathValue("name"), req, func(string, string) {})
	if err != nil {
		writePipelineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writePipelineError(w http.ResponseWriter, err error) {
	if pe, ok := err.(*pipelineError); ok {
		if pe.code == "collection_failed" {
			writeSourceError(w, pe.err, "pod_not_found")
			return
		}
		writeError(w, pe.status, pe.code, pe.err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
}

// handleTroubleshootStream runs the same pipeline but streams progress
// as Server-Sent Events:
//
//	data: {"stage":"collecting","message":"..."}     (one per stage)
//	event: done   data: <TroubleshootResult JSON>
//	event: error  data: {"code":"...","message":"..."}
//
// SSE fits because progress flows one way; it's plain HTTP, so it works
// through proxies, and browsers consume it natively. It accepts GET
// (query params, for EventSource) and POST (JSON body + Authorization
// header, for fetch-based clients and remote clusters).
func (s *Server) handleTroubleshootStream(w http.ResponseWriter, r *http.Request, src clusterSource, clusterKey string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support flushing")
		return
	}
	req, err := parseTroubleshootRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // stop nginx buffering the stream
	w.WriteHeader(http.StatusOK)

	// The pipeline goroutine and the heartbeat both write; closed stops
	// the goroutine touching w after this handler has returned (e.g. the
	// client disconnected mid-pipeline).
	var mu sync.Mutex
	closed := false
	defer func() {
		mu.Lock()
		closed = true
		mu.Unlock()
	}()
	write := func(event string, payload any) {
		data, _ := json.Marshal(payload)
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.TroubleshootTimeout)
	defer cancel()

	type outcome struct {
		result *model.TroubleshootResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := s.runTroubleshoot(ctx, src, clusterKey, r.PathValue("ns"), r.PathValue("name"), req, func(stage, detail string) {
			write("", map[string]string{"stage": stage, "message": detail})
		})
		done <- outcome{res, err}
	}()

	// Heartbeat comments keep idle-timeout proxies from cutting the
	// stream while the LLM call is in progress.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case out := <-done:
			if out.err != nil {
				code, msg := "internal_error", out.err.Error()
				if pe, ok := out.err.(*pipelineError); ok {
					code = pe.code
				}
				write("error", errorEnvelope{Code: code, Message: msg})
				return
			}
			write("done", out.result)
			return
		case <-heartbeat.C:
			mu.Lock()
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
			mu.Unlock()
		case <-r.Context().Done():
			return
		}
	}
}

// needsRelatedCheck decides whether pass-2 collection is worth running:
// a Service mismatch is plausible whenever the pod otherwise looks
// Healthy/Unknown (a container can be perfectly happy while its Service
// has zero endpoints), when ReadinessFailure keeps it out of endpoints,
// or when the caller named a Service; ConfigMap key evidence matters
// for CreateContainerConfigError.
func needsRelatedCheck(core model.DetectorResult, explicitServiceName *string) bool {
	if explicitServiceName != nil && *explicitServiceName != "" {
		return true
	}
	switch core.FailureType {
	case "", "Healthy", "Unknown", "CreateContainerConfigError":
		return true
	default:
		return false
	}
}

// normalizeDiagnosis guarantees required arrays are [] rather than null
// on the way out, whatever the AI service sent.
func normalizeDiagnosis(d *model.DiagnosisResponse) {
	if d.ObservedEvidence == nil {
		d.ObservedEvidence = []string{}
	}
	if d.ResolutionSteps == nil {
		d.ResolutionSteps = []model.ResolutionStep{}
	}
	if d.VerificationCommands == nil {
		d.VerificationCommands = []string{}
	}
	if d.DocumentationReferences == nil {
		d.DocumentationReferences = []model.DocumentationRef{}
	}
	if d.UncertaintyNotes == nil {
		d.UncertaintyNotes = []string{}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
