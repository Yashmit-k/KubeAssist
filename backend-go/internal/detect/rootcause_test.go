package detect

import (
	"testing"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

func TestRootCause(t *testing.T) {
	snap := func(ft string, facts map[string]string) *model.DiagnosticSnapshot {
		return &model.DiagnosticSnapshot{
			Pod:      model.PodInfo{Name: "p-1", Namespace: "lab"},
			Detector: model.DetectorResult{FailureType: ft, Fix: &model.FixPlan{Facts: facts}},
		}
	}
	for _, tc := range []struct {
		s          *model.DiagnosticSnapshot
		key, title string
	}{
		{snap("OOMKilled", map[string]string{"owner": "deployment/oom", "container": "app", "memoryLimit": "32Mi"}),
			"oom:lab/deployment/oom/app", `deployment/oom is OOMKilled: memory limit 32Mi is too low (container "app")`},
		{snap("ImagePullBackOff", map[string]string{"image": "r.invalid/x:1", "registry": "r.invalid", "pullProblem": `registry host "r.invalid" does not resolve`}),
			"registry-dns:r.invalid", `Registry host "r.invalid" does not exist`},
		// Env reference and volume mount of the same missing ConfigMap share a group.
		{snap("CreateContainerConfigError", map[string]string{"missingConfigMap": "cfg"}), "configmap:lab/cfg", `ConfigMap "cfg" does not exist`},
		{snap("VolumeMountFailure", map[string]string{"missingConfigMap": "cfg", "volume": "v"}), "configmap:lab/cfg", `ConfigMap "cfg" does not exist`},
		{snap("ReadinessFailure", map[string]string{"owner": "deployment/web", "container": "web", "probe": "readiness", "probeError": "Readiness probe failed: HTTP probe failed with statuscode: 404"}),
			"probe:lab/deployment/web/web/readiness", "deployment/web's readiness probe fails: HTTP 404"},
		{snap("CrashLoopBackOff", map[string]string{"owner": "deployment/api", "container": "api", "lastErrorLogLine": "FATAL: bad config"}),
			"crash:lab/deployment/api/api", `deployment/api keeps crashing: "FATAL: bad config"`},
	} {
		key, title := RootCause(tc.s)
		if key != tc.key || title != tc.title {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.s.Detector.FailureType, key, title, tc.key, tc.title)
		}
	}
}
