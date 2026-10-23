package detect

import (
	"strings"
	"testing"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// These tests are "golden" in the sense the plan means: hand-built
// fixture snapshots representing each of the 8 lab scenarios, with the
// EXPECTED classification asserted exactly. This is possible ONLY
// because Detect() is a pure function — no cluster, no network, no
// mocking required. This whole test file runs in milliseconds.
func TestDetect(t *testing.T) {
	tests := []struct {
		name     string
		snapshot *model.DiagnosticSnapshot
		want     string // expected FailureType
	}{
		{
			name: "ImagePullBackOff",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				Containers: []model.ContainerInfo{
					{Name: "app", State: model.ContainerState{Phase: "waiting", Reason: "ImagePullBackOff", Message: "no such image"}},
				},
			},
			want: "ImagePullBackOff",
		},
		{
			// Regression test for a real bug found by diffing against
			// the published diagnosis.schema.json: the schema explicitly
			// states "ErrImagePull is not a type" — Go must ALWAYS
			// normalize to "ImagePullBackOff" as failureType, even when
			// Kubernetes' own container state reason is literally
			// "ErrImagePull" (the earlier half of the same retry cycle,
			// before Kubernetes escalates to "ImagePullBackOff").
			// Previously this returned the raw reason string unchanged,
			// which would have emitted an invalid enum value straight
			// into the wire contract with Python.
			name: "ErrImagePull normalizes to ImagePullBackOff (schema regression)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				Containers: []model.ContainerInfo{
					{Name: "app", State: model.ContainerState{Phase: "waiting", Reason: "ErrImagePull", Message: "manifest unknown"}},
				},
			},
			want: "ImagePullBackOff",
		},
		{
			name: "OOMKilled",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{
						Name:         "app",
						RestartCount: 3,
						State:        model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"},
						LastState:    &model.ContainerState{Phase: "terminated", Reason: "OOMKilled"},
						Resources:    model.ResourceSummary{LimitsMemory: "128Mi"},
					},
				},
			},
			// OOMKilled must win even though the CURRENT waiting reason
			// is CrashLoopBackOff — this is exactly why detectOOMKilled
			// runs BEFORE detectCrashLoopBackOff in the priority list,
			// and this test would catch an accidental reordering.
			want: "OOMKilled",
		},
		{
			name: "CrashLoopBackOff (plain app crash)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{
						Name:         "app",
						RestartCount: 5,
						State:        model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"},
						LastState:    &model.ContainerState{Phase: "terminated", Reason: "Error", ExitCode: intPtr(1)},
					},
				},
			},
			want: "CrashLoopBackOff",
		},
		{
			// Regression test mirroring the init-container fix above:
			// same real-world race applies to regular containers —
			// caught mid-cycle in the terminated/Error half instead of
			// waiting/CrashLoopBackOff.
			name: "CrashLoopBackOff (terminated/Error half of restart cycle)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{
						Name:         "app",
						RestartCount: 4,
						State:        model.ContainerState{Phase: "terminated", Reason: "Error", ExitCode: intPtr(1)},
					},
				},
			},
			want: "CrashLoopBackOff",
		},
		{
			name: "ProbeFailure (not plain CrashLoop)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{
						Name:         "app",
						RestartCount: 2,
						State:        model.ContainerState{Phase: "running"},
						Probes:       model.ProbeSummary{Liveness: "HTTP GET /healthz:8080"},
					},
				},
				Events: []model.Event{
					{Type: "Warning", Reason: "Unhealthy", Message: "Liveness probe failed: connection refused"},
				},
			},
			want: "ProbeFailure",
		},
		{
			name: "CreateContainerConfigError",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				Containers: []model.ContainerInfo{
					{Name: "app", State: model.ContainerState{Phase: "waiting", Reason: "CreateContainerConfigError", Message: "configmap \"missing-cm\" not found"}},
				},
			},
			want: "CreateContainerConfigError",
		},
		{
			name: "FailedScheduling",
			snapshot: &model.DiagnosticSnapshot{
				Pod:    model.PodInfo{Phase: "Pending"},
				Events: []model.Event{{Type: "Warning", Reason: "FailedScheduling", Message: "0/1 nodes are available: insufficient memory"}},
			},
			want: "FailedScheduling",
		},
		{
			name: "Healthy",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{Name: "app", Ready: true, State: model.ContainerState{Phase: "running"}},
				},
			},
			want: "Healthy",
		},
		{
			name:     "Unknown (no containers, weird phase)",
			snapshot: &model.DiagnosticSnapshot{Pod: model.PodInfo{Phase: "Unknown"}},
			want:     "Unknown",
		},
		{
			name: "Evicted",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{
					Phase:   "Failed",
					Reason:  "Evicted",
					Message: "The node was low on resource: ephemeral-storage.",
				},
			},
			want: "Evicted",
		},
		{
			name: "InitContainerFailure (crash)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				InitContainers: []model.ContainerInfo{
					{
						Name:         "wait-for-db",
						RestartCount: 4,
						State:        model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"},
					},
				},
			},
			want: "InitContainerFailure",
		},
		{
			name: "InitContainerFailure (image pull)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				InitContainers: []model.ContainerInfo{
					{Name: "migrate", Image: "myregistry/migrate:typo-tag", State: model.ContainerState{Phase: "waiting", Reason: "ErrImagePull", Message: "manifest not found"}},
				},
			},
			want: "InitContainerFailure",
		},
		{
			// Regression test for a real bug found via live-cluster
			// testing: this is the EXACT snapshot shape a real
			// crash-looping init container produced (RestartCount: 6,
			// state terminated/Error) when curled at the "wrong" instant
			// in Kubernetes' restart cycle — before this fix, it was
			// misclassified as Unknown.
			name: "InitContainerFailure (terminated/Error half of restart cycle — real bug regression)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Pending"},
				InitContainers: []model.ContainerInfo{
					{
						Name:         "wait-for-db",
						RestartCount: 6,
						State:        model.ContainerState{Phase: "terminated", Reason: "Error", ExitCode: intPtr(1)},
						LastState:    &model.ContainerState{Phase: "terminated", Reason: "Error", ExitCode: intPtr(1)},
					},
				},
				Containers: []model.ContainerInfo{
					{Name: "app", Ready: false, State: model.ContainerState{Phase: "waiting", Reason: "PodInitializing"}},
				},
			},
			want: "InitContainerFailure",
		},
		{
			name: "VolumeMountFailure",
			snapshot: &model.DiagnosticSnapshot{
				Pod:    model.PodInfo{Phase: "Pending"},
				Events: []model.Event{{Type: "Warning", Reason: "FailedMount", Message: "MountVolume.SetUp failed for volume \"config\" : configmap \"missing-cm\" not found"}},
			},
			want: "VolumeMountFailure",
		},
		{
			name: "ReadinessFailure (no restart, never Ready)",
			snapshot: &model.DiagnosticSnapshot{
				Pod: model.PodInfo{Phase: "Running"},
				Containers: []model.ContainerInfo{
					{Name: "app", Ready: false, RestartCount: 0, State: model.ContainerState{Phase: "running"}},
				},
				Events: []model.Event{{Type: "Warning", Reason: "Unhealthy", Message: "Readiness probe failed: connection refused"}},
			},
			want: "ReadinessFailure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(tt.snapshot)
			if got.FailureType != tt.want {
				t.Errorf("Detect() FailureType = %q, want %q (evidence: %v)", got.FailureType, tt.want, got.Evidence)
			}
			if len(got.Evidence) == 0 {
				t.Errorf("Detect() returned no evidence for FailureType %q — every classification must cite evidence", got.FailureType)
			}
		})
	}
}

// TestDetectRelated_ServiceSelectorMismatch tests the pass-2 detector
// separately since it has a different function signature (it needs an
// already-computed core result, per the two-pass design).
func TestDetectRelated_ServiceSelectorMismatch(t *testing.T) {
	snapshot := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{
			Phase:  "Running",
			Labels: map[string]string{"app": "service-demo"},
		},
		Containers: []model.ContainerInfo{{Name: "app", Ready: true, State: model.ContainerState{Phase: "running"}}},
		Related: &model.RelatedInfo{
			Services: []model.ServiceSummary{
				{Name: "broken-service", Selector: map[string]string{"app": "wrong-label"}, EndpointsReady: 0},
			},
		},
	}

	core := Detect(snapshot) // would classify Healthy on its own
	if core.FailureType != "Healthy" {
		t.Fatalf("precondition failed: expected core classification Healthy, got %q", core.FailureType)
	}

	final := DetectRelated(snapshot, core)
	if final.FailureType != "ServiceSelectorMismatch" {
		t.Errorf("DetectRelated() FailureType = %q, want %q", final.FailureType, "ServiceSelectorMismatch")
	}
}

func intPtr(i int32) *int32 { return &i }

// TestDetect_InitContainerFailureTakesPriority verifies that a failing
// init container is correctly classified even when regular containers
// look perfectly healthy — proving detectInitContainerFailure's early
// position in the priority list actually matters, not just that the
// function works when tested alone. In practice, a pod with a failing
// init container will show regular containers as never-started (empty
// Containers slice, or all Ready:false with no State set), but this
// test deliberately gives them a "Healthy-looking" shape to prove the
// init-container check wins regardless.
func TestDetect_InitContainerFailureTakesPriority(t *testing.T) {
	snapshot := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{Phase: "Pending"},
		InitContainers: []model.ContainerInfo{
			{Name: "wait-for-db", RestartCount: 6, State: model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"}},
		},
		Containers: []model.ContainerInfo{
			{Name: "app", Ready: false, State: model.ContainerState{Phase: "waiting", Reason: "PodInitializing"}},
		},
	}

	got := Detect(snapshot)
	if got.FailureType != "InitContainerFailure" {
		t.Errorf("Detect() FailureType = %q, want %q — init container failure should take priority", got.FailureType, "InitContainerFailure")
	}
}

// Regression tests from live Kind testing: unrelated zero-endpoint
// Services in the same namespace must not be blamed.

func TestDetectRelated_IgnoresUnrelatedServicesWhenPodIsServed(t *testing.T) {
	snapshot := &model.DiagnosticSnapshot{
		Pod:        model.PodInfo{Phase: "Running", Labels: map[string]string{"app": "healthy-app"}},
		Containers: []model.ContainerInfo{{Name: "web", Ready: true, State: model.ContainerState{Phase: "running"}}},
		Related: &model.RelatedInfo{Services: []model.ServiceSummary{
			{Name: "healthy-app", Selector: map[string]string{"app": "healthy-app"}, EndpointsReady: 1},
			{Name: "broken-service", Selector: map[string]string{"app": "wrong-label"}, EndpointsReady: 0},
		}},
	}
	if got := DetectRelated(snapshot, Detect(snapshot)).FailureType; got != "Healthy" {
		t.Errorf("pod served by its own Service should stay Healthy, got %q", got)
	}
}

func TestDetectRelated_PodLevelFailureWins(t *testing.T) {
	snapshot := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{Phase: "Pending", Labels: map[string]string{"app": "missing-config-app"}},
		Containers: []model.ContainerInfo{{Name: "app", State: model.ContainerState{
			Phase: "waiting", Reason: "CreateContainerConfigError", Message: `couldn't find key DATABASE_URL in ConfigMap lab/application-config`,
		}}},
		Related: &model.RelatedInfo{
			Services:      []model.ServiceSummary{{Name: "broken-service", Selector: map[string]string{"app": "wrong-label"}}},
			ConfigMapRefs: []model.ConfigMapRef{{Name: "application-config", KeysPresent: []string{"LOG_LEVEL"}, KeysMissing: []string{"DATABASE_URL"}}},
		},
	}
	got := DetectRelated(snapshot, Detect(snapshot))
	if got.FailureType != "CreateContainerConfigError" {
		t.Fatalf("pod-level failure must win, got %q", got.FailureType)
	}
	found := false
	for _, e := range got.Evidence {
		if strings.Contains(e, "DATABASE_URL") && strings.Contains(e, "application-config") {
			found = true
		}
	}
	if !found {
		t.Errorf("pass-2 should add the missing ConfigMap key to the evidence: %v", got.Evidence)
	}
}

func TestDetectRelated_ExplicitServiceName(t *testing.T) {
	svc := "checkout"
	snapshot := &model.DiagnosticSnapshot{
		Pod:         model.PodInfo{Phase: "Running", Labels: map[string]string{"app": "checkout-v2"}},
		Containers:  []model.ContainerInfo{{Name: "web", Ready: true, State: model.ContainerState{Phase: "running"}}},
		ServiceName: &svc,
		Related: &model.RelatedInfo{Services: []model.ServiceSummary{
			{Name: "checkout", Selector: map[string]string{"tier": "checkout"}, EndpointsReady: 0},
		}},
	}
	if got := DetectRelated(snapshot, Detect(snapshot)).FailureType; got != "ServiceSelectorMismatch" {
		t.Errorf("named Service that doesn't select the pod should be a mismatch, got %q", got)
	}
}

func TestDetect_AddsLastErrorLogLine(t *testing.T) {
	prev := "Application starting\nconnecting to db\nFATAL: database configuration is invalid\n"
	snapshot := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{Phase: "Running"},
		Containers: []model.ContainerInfo{{Name: "app", RestartCount: 3,
			State: model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"}}},
		Logs: model.LogBundle{Previous: &prev},
	}
	got := Detect(snapshot)
	last := got.Evidence[len(got.Evidence)-1]
	if last != "Log: FATAL: database configuration is invalid" {
		t.Errorf("want the fatal line as evidence, got %q", last)
	}
}

// The kubelet's "unable to retrieve container logs" reply (returned while
// a container restarts) is not the app's error and must be skipped.
func TestLastErrorLogLine_SkipsKubeletLogErrors(t *testing.T) {
	prev := "checking database...\nfatal: database unreachable\n"
	s := &model.DiagnosticSnapshot{Logs: model.LogBundle{
		Previous: &prev,
		Current:  "unable to retrieve container logs for containerd://41bab8e31e8b",
	}}
	if got := lastErrorLogLine(s); got != "fatal: database unreachable" {
		t.Errorf("got %q", got)
	}
	only := &model.DiagnosticSnapshot{Logs: model.LogBundle{Current: "unable to retrieve container logs for containerd://abc"}}
	if got := lastErrorLogLine(only); got != "" {
		t.Errorf("kubelet error must not be quoted, got %q", got)
	}
}

// After a node/Docker restart every container restarts once with last
// termination reason "Unknown". That must not hide a readiness failure,
// while a real app crash still must.
func TestDetect_ReadinessFailureSurvivesNodeRestart(t *testing.T) {
	code := int32(255)
	snap := func(lastReason string) *model.DiagnosticSnapshot {
		return &model.DiagnosticSnapshot{
			Pod:    model.PodInfo{Phase: "Running"},
			Events: []model.Event{{Reason: "Unhealthy", Message: "Readiness probe failed: HTTP probe failed with statuscode: 404"}},
			Containers: []model.ContainerInfo{{Name: "web", RestartCount: 5,
				State:     model.ContainerState{Phase: "running"},
				LastState: &model.ContainerState{Phase: "terminated", Reason: lastReason, ExitCode: &code}}},
		}
	}
	if got := Detect(snap("Unknown")); got.FailureType != "ReadinessFailure" || !strings.Contains(got.Evidence[1], "5 restarts, all from node/runtime restarts") {
		t.Errorf("node restart: %+v", got)
	}
	if got := Detect(snap("Error")); got.FailureType == "ReadinessFailure" {
		t.Errorf("app crashes must not be called a readiness failure: %+v", got)
	}
}
