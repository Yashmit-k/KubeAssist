package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
)

const (
	// maxScanPods bounds one scan's work (each pod is a full pass-1
	// collection, through the agent for remote clusters).
	maxScanPods     = 40
	scanConcurrency = 4
	scanTimeout     = 90 * time.Second
)

type scanPod struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Ready        string `json:"ready"`
	RestartCount int32  `json:"restartCount"`
}

// scanGroup is one root cause and every pod it breaks. Fix is the
// evidence-derived fix of the first pod, which by construction of the
// group key applies to all of them.
type scanGroup struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	FailureType string         `json:"failureType"`
	Pods        []scanPod      `json:"pods"`
	Evidence    []string       `json:"evidence"`
	Fix         *model.FixPlan `json:"fix,omitempty"`
}

type scanError struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Error     string `json:"error"`
}

type scanResponse struct {
	Namespace string      `json:"namespace,omitempty"`
	ScannedAt time.Time   `json:"scannedAt"`
	TotalPods int         `json:"totalPods"`
	Checked   int         `json:"checked"`   // pods collected and classified
	Truncated bool        `json:"truncated"` // more candidates than maxScanPods
	Healthy   int         `json:"healthy"`   // candidates that turned out fine
	Groups    []scanGroup `json:"groups"`
	Errors    []scanError `json:"errors"`
}

// handleScan is the cluster health scan: find every pod that isn't
// cleanly Running, classify each with the detector, plan its fix, and
// group pods by root cause so a shared cause shows up once with one fix.
// No AI call is made — it is fast and free; the per-pod troubleshoot
// stays available for the full write-up.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request, src clusterSource, _ string) {
	ctx, cancel := context.WithTimeout(r.Context(), scanTimeout)
	defer cancel()
	resp, err := runScan(ctx, src, r.URL.Query().Get("namespace"))
	if err != nil {
		writeSourceError(w, err, "namespace_not_found")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// runScan is the scan itself, shared with the chat's scan tool.
func runScan(ctx context.Context, src clusterSource, namespace string) (*scanResponse, error) {
	pods, err := src.ListPods(ctx, namespace)
	if err != nil {
		return nil, err
	}

	candidates := make([]model.PodSummary, 0, len(pods))
	for _, p := range pods {
		if needsScan(p) {
			candidates = append(candidates, p)
		}
	}
	resp := scanResponse{Namespace: namespace, ScannedAt: time.Now().UTC(), TotalPods: len(pods), Groups: []scanGroup{}, Errors: []scanError{}}
	if len(candidates) > maxScanPods {
		candidates, resp.Truncated = candidates[:maxScanPods], true
	}

	type finding struct {
		pod  model.PodSummary
		snap *model.DiagnosticSnapshot
	}
	var (
		mu       sync.Mutex
		findings []finding
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(scanConcurrency)
	for _, p := range candidates {
		g.Go(func() error {
			snap, err := src.CollectCore(gctx, p.Namespace, p.Name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !isNotFound(err) { // a pod deleted mid-scan is not an error
					resp.Errors = append(resp.Errors, scanError{p.Namespace, p.Name, truncate(err.Error(), 200)})
				}
				return nil
			}
			snap.Detector = detect.Detect(snap)
			detect.Enrich(snap)
			redact.Snapshot(snap)
			findings = append(findings, finding{p, snap})
			return nil
		})
	}
	_ = g.Wait() // workers never return errors; failures are per pod

	byKey := map[string]*scanGroup{}
	for _, f := range findings {
		resp.Checked++
		ft := f.snap.Detector.FailureType
		if ft == "Healthy" {
			resp.Healthy++
			continue
		}
		key, title := detect.RootCause(f.snap)
		grp, ok := byKey[key]
		if !ok {
			grp = &scanGroup{Key: key, Title: title, FailureType: ft, Fix: f.snap.Detector.Fix, Evidence: firstN(f.snap.Detector.Evidence, 3)}
			byKey[key] = grp
		}
		grp.Pods = append(grp.Pods, scanPod{f.pod.Namespace, f.pod.Name, f.pod.Status, f.pod.Ready, f.pod.RestartCount})
	}
	for _, grp := range byKey {
		sort.Slice(grp.Pods, func(i, j int) bool {
			return grp.Pods[i].Namespace+"/"+grp.Pods[i].Name < grp.Pods[j].Namespace+"/"+grp.Pods[j].Name
		})
		resp.Groups = append(resp.Groups, *grp)
	}
	// Biggest blast radius first; then a stable order.
	sort.Slice(resp.Groups, func(i, j int) bool {
		a, b := resp.Groups[i], resp.Groups[j]
		if len(a.Pods) != len(b.Pods) {
			return len(a.Pods) > len(b.Pods)
		}
		return a.Title < b.Title
	})
	sort.Slice(resp.Errors, func(i, j int) bool { return resp.Errors[i].Name < resp.Errors[j].Name })
	return &resp, nil
}

// needsScan picks pods worth a full collection: anything not Running
// and fully Ready, or Running with restarts (probe failures and crash
// loops can look Running between restarts). Finished pods are skipped.
func needsScan(p model.PodSummary) bool {
	switch p.Status {
	case "Completed", "Succeeded":
		return false
	case "Running":
		ready, total, ok := strings.Cut(p.Ready, "/")
		return !ok || ready != total || p.RestartCount > 0
	}
	return true
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append([]string{}, s[:n]...)
	}
	return append([]string{}, s...)
}
