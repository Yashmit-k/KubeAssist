// Package detect classifies a DiagnosticSnapshot into one of the 8
// failure types the plan defines, WITHOUT ever calling out to the
// network or an LLM. This is a deliberate architectural boundary: the
// plan's Rule #3 is "Do not let the LLM classify failure type" — Go
// commits to a classification FIRST, using simple, explainable,
// deterministic logic, and the LLM is only ever allowed to explain and
// elaborate on what Go already decided. This buys three things:
//  1. Speed — no LLM round-trip needed just to know "this is OOMKilled".
//  2. Determinism — the same snapshot always produces the same
//     failureType, which is what makes automated testing possible at all.
//  3. Trust — a wrong LLM classification could send someone down a
//     completely wrong remediation path; a wrong Go classification is at
//     least consistent and debuggable, since it's plain if/else logic
//     you can read top to bottom.
package detect

import (
	"fmt"
	"strings"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// Detect runs every classifier in priority order and returns the first
// match, or "Healthy"/"Unknown" if nothing matches.
//
// WHY ORDERED PRIORITY RATHER THAN "MOST EVIDENCE WINS"?
// Some failure signatures are strict SUBSETS of others — e.g. a
// CrashLoopBackOff pod will ALSO show a generic "BackOff" event, and a
// ProbeFailure will also show container restarts that look superficially
// like a CrashLoopBackOff. Rather than building a scoring system (which
// would itself need tuning and could behave unpredictably), we check the
// MOST SPECIFIC signal first and return immediately on a match. This is
// simple enough to read start to finish and reason about by hand — a
// real virtue for a system whose entire point is to be trustworthy.
func Detect(s *model.DiagnosticSnapshot) model.DetectorResult {
	checks := []func(*model.DiagnosticSnapshot) *model.DetectorResult{
		detectEvicted,
		detectInitContainerFailure,
		detectImagePull,
		detectOOMKilled,
		detectVolumeMountFailure,
		detectCreateContainerConfigError,
		detectFailedScheduling,
		detectProbeFailure,
		detectCrashLoopBackOff,
		detectReadinessFailure,
		// ServiceSelectorMismatch is deliberately NOT in this list — it
		// needs model.RelatedInfo (Services/Endpoints), which is only
		// populated on "pass 2" of collection. See DetectRelated below.
	}

	for _, check := range checks {
		if result := check(s); result != nil {
			return *result
		}
	}

	return healthyOrUnknown(s)
}

// DetectRelated runs AFTER pass-2 collection has populated
// snapshot.Related, and can override an earlier "Healthy" verdict if a
// service mismatch is found. This two-stage design mirrors the
// collector's own two-pass algorithm (see the sequence diagram in the
// plan): we don't fetch Services/Endpoints for every pod, only when
// warranted, so detection has to be able to run twice too.
func DetectRelated(s *model.DiagnosticSnapshot, core model.DetectorResult) model.DetectorResult {
	if s.Related == nil {
		return core
	}
	for _, svc := range s.Related.Services {
		if svc.EndpointsReady == 0 && !podMatchesSelector(s.Pod.Labels, svc.Selector) {
			return model.DetectorResult{
				FailureType: "ServiceSelectorMismatch",
				Confidence:  0.85,
				Evidence: []string{
					fmt.Sprintf("Service %q has 0 ready endpoints", svc.Name),
					fmt.Sprintf("Service selector %v does not match pod labels %v", svc.Selector, s.Pod.Labels),
				},
				SearchTerms: []string{"service selector mismatch", "endpoints", "service discovery"},
			}
		}
	}
	return core
}

// detectEvicted checks for NODE-level eviction — Kubernetes sets
// pod.Status.Reason = "Evicted" when the kubelet terminates a pod
// because the NODE itself is under memory/disk/PID pressure, not
// because any single container hit ITS OWN limit. This is genuinely
// different from OOMKilled: OOMKilled is a container hitting the limit
// YOU set for it; Evicted means the whole node ran low on a resource
// and the kubelet had to reclaim capacity by killing lower-priority
// pods, regardless of what limits those pods declared. Checked FIRST
// (highest priority) because an evicted pod's containers may show
// confusing/incomplete state — the eviction reason is definitive and
// should not be shadowed by a container-level guess.
func detectEvicted(s *model.DiagnosticSnapshot) *model.DetectorResult {
	if s.Pod.Reason != "Evicted" {
		return nil
	}
	return &model.DetectorResult{
		FailureType: "Evicted",
		Confidence:  0.95,
		Evidence:    []string{fmt.Sprintf("Pod evicted: %s", s.Pod.Message)},
		SearchTerms: []string{"pod eviction", "node pressure", "DiskPressure", "MemoryPressure", "resource requests"},
	}
}

// detectInitContainerFailure checks init containers SEPARATELY from
// regular containers — a failing init container blocks the pod
// indefinitely (regular containers never even start), so this needs
// its own detector rather than reusing detectCrashLoopBackOff/
// detectImagePull, which only ever look at s.Containers. Checked early
// (second priority) since an init-container failure means everything
// downstream about regular containers is moot — they haven't started.
//
// IMPORTANT: a crash-looping container cycles through states over
// time — Kubernetes doesn't hold it in one fixed state. The cycle is
// roughly: running -> terminated(Error) -> [brief pause] ->
// waiting(CrashLoopBackOff) -> running again -> repeat. A single
// snapshot only catches WHICHEVER instant we happened to poll at, so
// this detector checks BOTH halves of that cycle — waiting/
// CrashLoopBackOff (caught during the backoff pause) AND
// terminated/Error with RestartCount > 0 (caught right after a failed
// attempt, before the backoff wait even begins). Checking only the
// first half was a real bug we found via live-cluster testing: a real
// init container was correctly restarting and failing (RestartCount:
// 6) but got classified as Unknown because we happened to sample it
// mid-cycle in the terminated/Error half, not the waiting half.
func detectInitContainerFailure(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, c := range s.InitContainers {
		if c.State.Phase == "waiting" && (c.State.Reason == "CrashLoopBackOff" || c.State.Reason == "Error") {
			return &model.DetectorResult{
				FailureType: "InitContainerFailure",
				Confidence:  0.9,
				Evidence: []string{
					fmt.Sprintf("Init container %q waiting: %s — %s (restarts: %d)",
						c.Name, c.State.Reason, c.State.Message, c.RestartCount),
				},
				SearchTerms: []string{"init container", "InitContainerStatuses", c.Name},
			}
		}
		if c.State.Phase == "waiting" && (c.State.Reason == "ImagePullBackOff" || c.State.Reason == "ErrImagePull") {
			return &model.DetectorResult{
				FailureType: "InitContainerFailure",
				Confidence:  0.9,
				Evidence: []string{
					fmt.Sprintf("Init container %q image pull failed: %s", c.Name, c.State.Message),
				},
				SearchTerms: []string{"init container", "ImagePullBackOff", c.Image},
			}
		}
		// Catches the OTHER half of the cycle: currently terminated
		// with a nonzero exit, but has already restarted at least
		// once — meaning this isn't a first-attempt failure still
		// working through its FIRST startup, it's an established
		// repeat failure we caught between backoff windows.
		if c.State.Phase == "terminated" && c.State.Reason == "Error" && c.RestartCount > 0 {
			exitCode := 0
			if c.State.ExitCode != nil {
				exitCode = int(*c.State.ExitCode)
			}
			return &model.DetectorResult{
				FailureType: "InitContainerFailure",
				Confidence:  0.9,
				Evidence: []string{
					fmt.Sprintf("Init container %q repeatedly failing: exit code %d (restarts: %d)",
						c.Name, exitCode, c.RestartCount),
				},
				SearchTerms: []string{"init container", "exit code", c.Name},
			}
		}
	}
	return nil
}

// detectVolumeMountFailure checks Events for FailedMount/
// FailedAttachVolume — one of the most common real causes of a pod
// stuck Pending indefinitely (typo'd ConfigMap/Secret/PVC name, PVC
// not yet bound, wrong storage class). These events fire at the POD
// level before any container even attempts to start, which is why this
// is checked via Events rather than container state — there IS no
// container state yet when this happens.
func detectVolumeMountFailure(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, e := range s.Events {
		if e.Reason == "FailedMount" || e.Reason == "FailedAttachVolume" {
			return &model.DetectorResult{
				FailureType: "VolumeMountFailure",
				Confidence:  0.9,
				Evidence:    []string{fmt.Sprintf("Event: %s — %s", e.Reason, e.Message)},
				SearchTerms: []string{"FailedMount", "PersistentVolumeClaim", "ConfigMap volume", "Secret volume"},
			}
		}
	}
	return nil
}

// detectReadinessFailure catches the case where a container is RUNNING
// and NOT restarting, but its readiness probe keeps failing — the pod
// never becomes Ready, but nothing crashes. This is genuinely different
// from detectProbeFailure (liveness probe killing/restarting the
// container) — here the container is healthy enough that Kubernetes
// never kills it, but the app itself is reporting "I'm not ready to
// serve traffic" (commonly: can't reach a dependency like a database).
// Checked LATE (after CrashLoopBackOff/ProbeFailure) since those are
// more specific/severe signals when both are present.
func detectReadinessFailure(s *model.DiagnosticSnapshot) *model.DetectorResult {
	hasUnhealthyReadinessEvent := false
	var msg string
	for _, e := range s.Events {
		if e.Reason == "Unhealthy" && strings.Contains(strings.ToLower(e.Message), "readiness") {
			hasUnhealthyReadinessEvent = true
			msg = e.Message
			break
		}
	}
	if !hasUnhealthyReadinessEvent {
		return nil
	}
	for _, c := range s.Containers {
		// The defining signature: container is RUNNING, has NOT
		// restarted (RestartCount == 0 — if it had restarted, this is
		// more likely ProbeFailure/CrashLoopBackOff territory, already
		// checked earlier), but is NOT Ready.
		if c.State.Phase == "running" && !c.Ready && c.RestartCount == 0 {
			return &model.DetectorResult{
				FailureType: "ReadinessFailure",
				Confidence:  0.8,
				Evidence: []string{
					fmt.Sprintf("Readiness probe failing: %s", msg),
					fmt.Sprintf("Container %q running, 0 restarts, but not Ready", c.Name),
				},
				SearchTerms: []string{"readiness probe", "dependency check", "database connection", "startup delay"},
			}
		}
	}
	return nil
}

func detectImagePull(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, c := range s.Containers {
		if c.State.Phase == "waiting" && (c.State.Reason == "ImagePullBackOff" || c.State.Reason == "ErrImagePull") {
			return &model.DetectorResult{
				// ALWAYS normalize to the canonical "ImagePullBackOff" —
				// never the raw Kubernetes reason string, which can also
				// legitimately be "ErrImagePull". Per
				// diagnosis.schema.json's own comment: "ErrImagePull is
				// not a type; Go must send ImagePullBackOff and put
				// ErrImagePull in evidence." Previously this returned
				// c.State.Reason directly, which meant a pod caught in
				// the ErrImagePull half of its retry cycle would emit an
				// invalid failureType value — a real bug, found by
				// diffing against the published schema rather than by
				// live testing (both test pods we happened to deploy
				// settled into the ImagePullBackOff half before we
				// checked).
				FailureType: "ImagePullBackOff",
				Confidence:  0.95,
				Evidence: []string{
					fmt.Sprintf("Container %q waiting: %s — %s", c.Name, c.State.Reason, c.State.Message),
				},
				SearchTerms: []string{"ImagePullBackOff", "image pull error", c.Image},
			}
		}
	}
	return nil
}

func detectOOMKilled(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, c := range s.Containers {
		reason := c.State.Reason
		if c.LastState != nil && c.LastState.Reason == "OOMKilled" {
			reason = "OOMKilled"
		}
		if reason == "OOMKilled" {
			return &model.DetectorResult{
				FailureType: "OOMKilled",
				Confidence:  0.95,
				Evidence: []string{
					fmt.Sprintf("Container %q was OOMKilled (restarts: %d, memory limit: %s)",
						c.Name, c.RestartCount, c.Resources.LimitsMemory),
				},
				SearchTerms: []string{"OOMKilled", "memory limit", "resource requests"},
			}
		}
	}
	return nil
}

func detectCreateContainerConfigError(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, c := range s.Containers {
		if c.State.Phase == "waiting" && c.State.Reason == "CreateContainerConfigError" {
			return &model.DetectorResult{
				FailureType: "CreateContainerConfigError",
				Confidence:  0.9,
				Evidence: []string{
					fmt.Sprintf("Container %q: %s", c.Name, c.State.Message),
				},
				SearchTerms: []string{"CreateContainerConfigError", "ConfigMap", "Secret", "missing key"},
			}
		}
	}
	return nil
}

func detectFailedScheduling(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, e := range s.Events {
		if e.Reason == "FailedScheduling" {
			return &model.DetectorResult{
				FailureType: "FailedScheduling",
				Confidence:  0.9,
				Evidence:    []string{fmt.Sprintf("Event: %s", e.Message)},
				SearchTerms: []string{"FailedScheduling", "node affinity", "resource requests", "taints"},
			}
		}
	}
	return nil
}

// detectProbeFailure distinguishes "the app itself is crashing" from
// "the app is fine but slow to start and the liveness probe is killing
// it prematurely" — a genuinely subtle distinction the plan calls out
// explicitly (Section 5: "CrashLoopBackOff (app exit vs probe-induced —
// use events Unhealthy + probe config)"). We check for an "Unhealthy"
// event (which only fires from a FAILED probe, never from the app
// exiting on its own) combined with a configured liveness probe.
func detectProbeFailure(s *model.DiagnosticSnapshot) *model.DetectorResult {
	hasUnhealthyEvent := false
	var unhealthyMsg string
	for _, e := range s.Events {
		if e.Reason == "Unhealthy" && strings.Contains(strings.ToLower(e.Message), "liveness") {
			hasUnhealthyEvent = true
			unhealthyMsg = e.Message
			break
		}
	}
	if !hasUnhealthyEvent {
		return nil
	}
	for _, c := range s.Containers {
		if c.Probes.Liveness != "" && c.RestartCount > 0 {
			return &model.DetectorResult{
				FailureType: "ProbeFailure",
				Confidence:  0.85,
				Evidence: []string{
					fmt.Sprintf("Liveness probe failing: %s", unhealthyMsg),
					fmt.Sprintf("Container %q liveness probe: %s", c.Name, c.Probes.Liveness),
				},
				SearchTerms: []string{"liveness probe", "startup probe", "probe timeout", "initialDelaySeconds"},
			}
		}
	}
	return nil
}

// detectCrashLoopBackOff checks BOTH halves of the crash-restart cycle,
// for the exact same reason documented in detectInitContainerFailure
// above: Kubernetes cycles a failing container between
// waiting(CrashLoopBackOff) and terminated(Error), and a single
// snapshot only catches whichever half we happened to poll during. This
// fix was discovered by finding the identical bug in
// detectInitContainerFailure via live-cluster testing — the same race
// applies here and was only luck (long backoff windows in our earlier
// manual test) that it hadn't bitten us yet.
func detectCrashLoopBackOff(s *model.DiagnosticSnapshot) *model.DetectorResult {
	for _, c := range s.Containers {
		if c.State.Phase == "waiting" && c.State.Reason == "CrashLoopBackOff" {
			evidence := []string{
				fmt.Sprintf("Container %q waiting: CrashLoopBackOff (restarts: %d)", c.Name, c.RestartCount),
			}
			if c.LastState != nil && c.LastState.ExitCode != nil {
				evidence = append(evidence,
					fmt.Sprintf("Last termination: exit code %d, reason %s", *c.LastState.ExitCode, c.LastState.Reason))
			}
			return &model.DetectorResult{
				FailureType: "CrashLoopBackOff",
				Confidence:  0.9,
				Evidence:    evidence,
				SearchTerms: []string{"CrashLoopBackOff", "container exit code", "application logs"},
			}
		}
		// The other half of the cycle: currently terminated with a
		// nonzero-exit "Error" reason, but has already restarted at
		// least once — an established repeat failure caught between
		// backoff windows, not a first-attempt failure.
		if c.State.Phase == "terminated" && c.State.Reason == "Error" && c.RestartCount > 0 {
			exitCode := 0
			if c.State.ExitCode != nil {
				exitCode = int(*c.State.ExitCode)
			}
			return &model.DetectorResult{
				FailureType: "CrashLoopBackOff",
				Confidence:  0.9,
				Evidence: []string{
					fmt.Sprintf("Container %q repeatedly failing: exit code %d (restarts: %d)",
						c.Name, exitCode, c.RestartCount),
				},
				SearchTerms: []string{"CrashLoopBackOff", "container exit code", "application logs"},
			}
		}
	}
	return nil
}

func healthyOrUnknown(s *model.DiagnosticSnapshot) model.DetectorResult {
	allReady := len(s.Containers) > 0
	for _, c := range s.Containers {
		if !c.Ready {
			allReady = false
			break
		}
	}
	if allReady && s.Pod.Phase == "Running" {
		return model.DetectorResult{
			FailureType: "Healthy",
			Confidence:  0.8,
			Evidence:    []string{"All containers ready, pod phase Running, no matching failure signatures"},
			SearchTerms: []string{},
		}
	}
	return model.DetectorResult{
		FailureType: "Unknown",
		Confidence:  0.3,
		Evidence:    []string{fmt.Sprintf("Pod phase %q with no matching failure signature", s.Pod.Phase)},
		SearchTerms: []string{s.Pod.Phase},
	}
}

// podMatchesSelector checks if a pod's labels satisfy a service's
// selector (every key in selector must exist in labels with a matching
// value — this mirrors exactly how Kubernetes' own Service->Endpoints
// controller matches pods).
func podMatchesSelector(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false // an empty selector matches nothing meaningfully for our purposes
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
