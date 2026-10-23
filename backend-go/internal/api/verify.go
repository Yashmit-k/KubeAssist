package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

type verifyRequest struct {
	// FailureType is what the pod was diagnosed with before the fix.
	FailureType string `json:"failureType"`
	// Owner is the workload ("deployment/api") from the fix facts. Most
	// fixes roll the pod out under a NEW name, so the check follows the
	// workload when the original pod is gone.
	Owner       string  `json:"owner,omitempty"`
	ServiceName *string `json:"serviceName,omitempty"`
}

// Verdicts, from the user's point of view.
const (
	verdictFixed    = "fixed"         // the pod is now Running and Ready
	verdictStarting = "starting"      // a new pod is still coming up; check again shortly
	verdictStill    = "still_failing" // same failure as before
	verdictNew      = "new_problem"   // a different failure: the fix moved things on
	verdictNotFound = "not_found"     // neither the pod nor its workload has pods
)

type verifyResponse struct {
	Verdict     string         `json:"verdict"`
	Message     string         `json:"message"`
	Pod         *scanPod       `json:"pod,omitempty"`
	Replaced    bool           `json:"replaced"` // the check followed the workload to a new pod
	FailureType string         `json:"failureType,omitempty"`
	Evidence    []string       `json:"evidence"`
	Fix         *model.FixPlan `json:"fix,omitempty"`
	CheckedAt   time.Time      `json:"checkedAt"`
}

// handleVerify answers "did my fix work?": it collects the pod again —
// or, when the fix replaced it, the newest pod of the same workload —
// classifies it and compares with the diagnosis the user acted on.
// Read-only like everything else; nothing is changed.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	ns, name := r.PathValue("ns"), r.PathValue("name")

	var req verifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "could not parse request body")
		return
	}

	resp := verifyResponse{CheckedAt: time.Now().UTC(), Evidence: []string{}}
	snap, err := src.CollectCore(ctx, ns, name)
	if err != nil && !isNotFound(err) {
		writeSourceError(w, err, "pod_not_found")
		return
	}
	if snap == nil {
		next, err := s.newestWorkloadPod(ctx, src, ns, req.Owner)
		if err != nil {
			writeSourceError(w, err, "namespace_not_found")
			return
		}
		if next == "" {
			resp.Verdict = verdictNotFound
			resp.Message = fmt.Sprintf("Pod %q no longer exists and no replacement was found", name)
			if req.Owner != "" {
				resp.Message += fmt.Sprintf(" for %s", req.Owner)
			}
			resp.Message += ". If the workload was deleted or scaled to zero, that is expected."
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if snap, err = src.CollectCore(ctx, ns, next); err != nil {
			writeSourceError(w, err, "pod_not_found")
			return
		}
		resp.Replaced = true
	}

	snap.ServiceName = req.ServiceName
	core := detect.Detect(snap)
	snap.Detector = core
	if req.ServiceName != nil && *req.ServiceName != "" {
		if related, err := src.CollectRelated(ctx, snap.Pod.Namespace, snap.Pod.Name, req.ServiceName); err == nil {
			snap.Related = related
			snap.Detector = detect.DetectRelated(snap, core)
		}
	}
	snap.Detector.Fix = detect.PlanFix(snap)
	redact.Snapshot(snap)

	d := snap.Detector
	resp.FailureType = d.FailureType
	resp.Evidence = firstN(d.Evidence, 4)
	resp.Pod = podState(snap)
	which := "The pod"
	if resp.Replaced {
		which = fmt.Sprintf("The new pod %q", snap.Pod.Name)
	}
	up, sinceStart := settled(snap, resp.CheckedAt)
	switch {
	case d.FailureType == "Healthy" && !up:
		// Running and Ready a few seconds after a restart proves little:
		// an OOM or crash typically comes moments later.
		resp.Verdict = verdictStarting
		resp.Message = fmt.Sprintf("%s is Running and Ready, but only for %s — checking again to confirm it stays up.", which, sinceStart.Round(time.Second))
	case d.FailureType == "Healthy":
		resp.Verdict = verdictFixed
		resp.Message = fmt.Sprintf("%s is Running and all containers are Ready.", which)
		if resp.Pod.RestartCount > 0 {
			resp.Message += fmt.Sprintf(" It has restarted %d time(s) so far; check again in a few minutes to confirm it stays up.", resp.Pod.RestartCount)
		}
	case starting(snap):
		resp.Verdict = verdictStarting
		resp.Message = fmt.Sprintf("%s is still starting (%s). Check again in a few seconds.", which, resp.Pod.Status)
	case d.FailureType == req.FailureType:
		resp.Verdict = verdictStill
		resp.Message = fmt.Sprintf("%s still shows %s. The fix hasn't taken effect, or didn't address the cause — see the evidence below.", which, d.FailureType)
		resp.Fix = d.Fix
	default:
		resp.Verdict = verdictNew
		resp.Message = fmt.Sprintf("%s no longer shows %s, but now shows %s. That's progress: the next problem is below.", which, nonEmptyStr(req.FailureType, "the earlier failure"), d.FailureType)
		resp.Fix = d.Fix
	}
	writeJSON(w, http.StatusOK, resp)
}

// newestWorkloadPod finds the most recently created pod belonging to
// owner ("deployment/api" -> pods named "api-<hash>-<id>").
func (s *Server) newestWorkloadPod(ctx context.Context, src clusterSource, ns, owner string) (string, error) {
	kind, base, ok := strings.Cut(owner, "/")
	if !ok || base == "" || kind == "pod" {
		return "", nil
	}
	pods, err := src.ListPods(ctx, ns)
	if err != nil {
		return "", err
	}
	best := model.PodSummary{}
	for _, p := range pods {
		if p.Namespace == ns && strings.HasPrefix(p.Name, base+"-") && p.CreatedAt.After(best.CreatedAt) {
			best = p
		}
	}
	return best.Name, nil
}

// stableAfter is how long containers must have been running before a
// healthy pod counts as fixed.
const stableAfter = 30 * time.Second

// settled reports whether every container has been running for at least
// stableAfter, and how long the most recently started one has run.
func settled(s *model.DiagnosticSnapshot, now time.Time) (bool, time.Duration) {
	shortest := time.Duration(-1)
	for _, c := range s.Containers {
		started, err := time.Parse(time.RFC3339, c.State.StartedAt)
		if c.State.Phase != "running" || err != nil {
			continue
		}
		if up := now.Sub(started); shortest < 0 || up < shortest {
			shortest = up
		}
	}
	if shortest < 0 {
		return true, 0 // no start times reported: don't block the verdict
	}
	return shortest >= stableAfter, shortest
}

// starting reports a pod that was just (re)created and hasn't settled:
// Unknown/Pending while containers are being created or initialized.
func starting(s *model.DiagnosticSnapshot) bool {
	if s.Detector.FailureType != "Unknown" {
		return false
	}
	if s.Pod.Phase == "Pending" {
		return true
	}
	for _, c := range append(append([]model.ContainerInfo{}, s.InitContainers...), s.Containers...) {
		switch c.State.Reason {
		case "ContainerCreating", "PodInitializing", "StateNotReported":
			return true
		}
	}
	return false
}

func podState(s *model.DiagnosticSnapshot) *scanPod {
	ready, restarts := 0, int32(0)
	status := s.Pod.Phase
	for _, c := range s.Containers {
		if c.Ready {
			ready++
		}
		restarts += c.RestartCount
		if c.State.Phase == "waiting" && c.State.Reason != "" {
			status = c.State.Reason
		}
	}
	return &scanPod{Namespace: s.Pod.Namespace, Name: s.Pod.Name, Status: status,
		Ready: fmt.Sprintf("%d/%d", ready, len(s.Containers)), RestartCount: restarts}
}

func nonEmptyStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
