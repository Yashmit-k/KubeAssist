package detect

import (
	"strings"
	"testing"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// The messages below are copied from the Kind lab (deploy/lab), so the
// parsing is tested against what Kubernetes actually reports.

func ptr32(v int32) *int32 { return &v }

func deployPod(name string) model.PodInfo {
	return model.PodInfo{Name: name + "-7c65c7ccc8-nzqj7", Namespace: "kubeassist-lab", Phase: "Pending",
		OwnerRefs: []model.OwnerRef{{Kind: "ReplicaSet", Name: name + "-7c65c7ccc8"}}}
}

func withDeployment(s *model.DiagnosticSnapshot, name string) *model.DiagnosticSnapshot {
	s.Owners = &model.OwnerInfo{Deployment: &model.DeploymentRef{Name: name}}
	return s
}

// writeCommand returns the command of the plan's first write step.
func writeCommand(t *testing.T, plan *model.FixPlan) string {
	t.Helper()
	if plan == nil {
		t.Fatal("no fix plan")
	}
	for _, st := range plan.Steps {
		if st.IsWriteAction {
			return st.Command
		}
	}
	t.Fatalf("no write step in %+v", plan.Steps)
	return ""
}

func commandWith(t *testing.T, plan *model.FixPlan, prefix string) string {
	t.Helper()
	for _, st := range plan.Steps {
		if strings.HasPrefix(st.Command, prefix) {
			return st.Command
		}
	}
	t.Fatalf("no %q step in %+v", prefix, plan.Steps)
	return ""
}

func TestPlanFix_OOMKilledDoublesTheRealLimit(t *testing.T) {
	s := withDeployment(&model.DiagnosticSnapshot{
		Pod: deployPod("oom-failure"),
		Containers: []model.ContainerInfo{{Name: "application", RestartCount: 57,
			State:     model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"},
			LastState: &model.ContainerState{Phase: "terminated", Reason: "OOMKilled", ExitCode: ptr32(137)},
			Resources: model.ResourceSummary{LimitsMemory: "32Mi"}}},
		Detector: model.DetectorResult{FailureType: "OOMKilled"},
	}, "oom-failure")
	plan := PlanFix(s)
	if got := writeCommand(t, plan); got != "kubectl set resources deployment/oom-failure -n kubeassist-lab -c application --limits=memory=64Mi" {
		t.Errorf("command = %q", got)
	}
	if plan.Facts["memoryLimit"] != "32Mi" || plan.Facts["suggestedMemoryLimit"] != "64Mi" {
		t.Errorf("facts = %v", plan.Facts)
	}
}

func TestPlanFix_MissingConfigMapKey(t *testing.T) {
	s := withDeployment(&model.DiagnosticSnapshot{
		Pod: deployPod("missing-config-app"),
		Containers: []model.ContainerInfo{{Name: "application", State: model.ContainerState{Phase: "waiting",
			Reason: "CreateContainerConfigError", Message: "couldn't find key database-url in ConfigMap kubeassist-lab/application-config"}}},
		Detector: model.DetectorResult{FailureType: "CreateContainerConfigError"},
	}, "missing-config-app")
	plan := PlanFix(s)
	want := `kubectl patch configmap application-config -n kubeassist-lab --type merge -p '{"data":{"database-url":"<value>"}}'`
	if got := writeCommand(t, plan); got != want {
		t.Errorf("command = %q\nwant      %q", got, want)
	}
	if plan.Facts["configMap"] != "application-config" || plan.Facts["missingKey"] != "database-url" {
		t.Errorf("facts = %v", plan.Facts)
	}
}

func TestPlanFix_MissingConfigMapEntirely_UsesKeysFromPass2(t *testing.T) {
	s := withDeployment(&model.DiagnosticSnapshot{
		Pod: deployPod("app"),
		Containers: []model.ContainerInfo{{Name: "app", State: model.ContainerState{Phase: "waiting",
			Reason: "CreateContainerConfigError", Message: `configmap "app-config" not found`}}},
		Related:  &model.RelatedInfo{ConfigMapRefs: []model.ConfigMapRef{{Name: "app-config", KeysMissing: []string{"DB_HOST", "DB_PORT"}}}},
		Detector: model.DetectorResult{FailureType: "CreateContainerConfigError"},
	}, "app")
	want := "kubectl create configmap app-config -n kubeassist-lab --from-literal=DB_HOST=<value> --from-literal=DB_PORT=<value>"
	if got := writeCommand(t, PlanFix(s)); got != want {
		t.Errorf("command = %q", got)
	}
}

func TestPlanFix_ImagePull(t *testing.T) {
	pull := func(image, msg string) *model.DiagnosticSnapshot {
		return withDeployment(&model.DiagnosticSnapshot{
			Pod: deployPod("image-pull-failure"),
			Containers: []model.ContainerInfo{{Name: "application", Image: image,
				State: model.ContainerState{Phase: "waiting", Reason: "ImagePullBackOff"}}},
			Events:   []model.Event{{Reason: "Failed", Message: `Failed to pull image "` + image + `": ` + msg}},
			Detector: model.DetectorResult{FailureType: "ImagePullBackOff"},
		}, "image-pull-failure")
	}

	plan := PlanFix(pull("registry.invalid.example/kubeassist/nonexistent:v999",
		`failed to resolve reference: dial tcp: lookup registry.invalid.example on 192.168.65.254:53: no such host`))
	if got := writeCommand(t, plan); got != "kubectl set image deployment/image-pull-failure -n kubeassist-lab application=<registry>/kubeassist/nonexistent:v999" {
		t.Errorf("dns: %q", got)
	}
	if !strings.Contains(plan.Facts["pullProblem"], `"registry.invalid.example" does not resolve`) {
		t.Errorf("dns facts: %v", plan.Facts)
	}

	plan = PlanFix(pull("nginx:does-not-exist", `docker.io/library/nginx:does-not-exist: not found`))
	if got := writeCommand(t, plan); got != "kubectl set image deployment/image-pull-failure -n kubeassist-lab application=nginx:<tag>" {
		t.Errorf("tag: %q", got)
	}

	plan = PlanFix(pull("ghcr.io/acme/private:1.0", `401 Unauthorized`))
	cmds := []string{}
	for _, st := range plan.Steps {
		cmds = append(cmds, st.Command)
	}
	joined := strings.Join(cmds, "\n")
	if !strings.Contains(joined, "kubectl create secret docker-registry regcred -n kubeassist-lab --docker-server=ghcr.io") ||
		!strings.Contains(joined, `"imagePullSecrets":[{"name":"regcred"}]`) {
		t.Errorf("auth steps: %s", joined)
	}
}

func TestPlanFix_VolumeMountMissingConfigMap_BarePod(t *testing.T) {
	s := &model.DiagnosticSnapshot{
		Pod:        model.PodInfo{Name: "volume-mount-failure", Namespace: "kubeassist-lab", Phase: "Pending"},
		Containers: []model.ContainerInfo{{Name: "app", State: model.ContainerState{Phase: "waiting", Reason: "ContainerCreating"}}},
		Events: []model.Event{{Reason: "FailedMount",
			Message: `MountVolume.SetUp failed for volume "config" : configmap "this-configmap-does-not-exist" not found`}},
		Detector: model.DetectorResult{FailureType: "VolumeMountFailure"},
	}
	plan := PlanFix(s)
	if got := writeCommand(t, plan); got != "kubectl create configmap this-configmap-does-not-exist -n kubeassist-lab --from-file=<path>" {
		t.Errorf("command = %q", got)
	}
	if plan.Facts["volume"] != "config" {
		t.Errorf("facts = %v", plan.Facts)
	}
}

func TestPlanFix_SchedulingShowsRealRequests(t *testing.T) {
	s := withDeployment(&model.DiagnosticSnapshot{
		Pod: deployPod("scheduling-failure"),
		Containers: []model.ContainerInfo{{Name: "application", State: model.ContainerState{Phase: "waiting", Reason: "StateNotReported"},
			Resources: model.ResourceSummary{Requests: map[string]string{"cpu": "1k", "memory": "1Ti"}}}},
		Events:   []model.Event{{Reason: "FailedScheduling", Message: "0/1 nodes are available: 1 Insufficient cpu, 1 Insufficient memory."}},
		Detector: model.DetectorResult{FailureType: "FailedScheduling"},
	}, "scheduling-failure")
	plan := PlanFix(s)
	if !strings.Contains(plan.Steps[0].Description, "cpu=1k, memory=1Ti") {
		t.Errorf("first step should name the real requests: %q", plan.Steps[0].Description)
	}
	if got := writeCommand(t, plan); !strings.HasPrefix(got, "kubectl set resources deployment/scheduling-failure -n kubeassist-lab -c application --requests=") {
		t.Errorf("command = %q", got)
	}
}

func TestPlanFix_Probes(t *testing.T) {
	probe := func(kind, summary, event string) *model.FixPlan {
		c := model.ContainerInfo{Name: "application", State: model.ContainerState{Phase: "running"}}
		ft := "ProbeFailure"
		if kind == "readiness" {
			c.Probes.Readiness = summary
			ft = "ReadinessFailure"
		} else {
			c.Probes.Liveness = summary
		}
		return PlanFix(withDeployment(&model.DiagnosticSnapshot{
			Pod: deployPod("probe-app"), Containers: []model.ContainerInfo{c},
			Events:   []model.Event{{Reason: "Unhealthy", Message: event}},
			Detector: model.DetectorResult{FailureType: ft},
		}, "probe-app"))
	}

	plan := probe("liveness", "HTTP GET /:8080 (initialDelay=2s period=3s timeout=1s failureThreshold=2)",
		`Liveness probe failed: Get "http://10.244.0.17:8080/": dial tcp 10.244.0.17:8080: connect: connection refused`)
	want := `kubectl patch deployment/probe-app -n kubeassist-lab -p '{"spec":{"template":{"spec":{"containers":[{"livenessProbe":{"httpGet":{"path":"/","port":<app-port>}},"name":"application"}]}}}}'`
	if got := commandWith(t, plan, "kubectl patch"); got != want {
		t.Errorf("refused:\n got %q\nwant %q", got, want)
	}
	if got := commandWith(t, plan, "kubectl exec"); got != "kubectl exec probe-app-7c65c7ccc8-nzqj7 -n kubeassist-lab -c application -- netstat -tln" {
		t.Errorf("port check: %q", got)
	}

	plan = probe("readiness", "HTTP GET /healthz/dependencies:80 (initialDelay=0s period=5s timeout=1s failureThreshold=1)",
		"Readiness probe failed: HTTP probe failed with statuscode: 404")
	if got := writeCommand(t, plan); !strings.Contains(got, `"readinessProbe":{"httpGet":{"path":"<health-path>","port":80}}`) {
		t.Errorf("404: %q", got)
	}
}

func TestPlanFix_ServiceSelectorPatch(t *testing.T) {
	svc := "service-demo"
	s := &model.DiagnosticSnapshot{
		Pod:         model.PodInfo{Name: "service-demo-b9564d6c8-njh6z", Namespace: "kubeassist-lab", Labels: map[string]string{"app": "service-demo"}},
		ServiceName: &svc,
		Related: &model.RelatedInfo{Services: []model.ServiceSummary{
			{Name: "service-demo", Selector: map[string]string{"app": "service-demo-v2"}, EndpointsReady: 0}}},
		Detector: model.DetectorResult{FailureType: "ServiceSelectorMismatch"},
	}
	want := `kubectl patch service service-demo -n kubeassist-lab -p '{"spec":{"selector":{"app":"service-demo"}}}'`
	if got := writeCommand(t, PlanFix(s)); got != want {
		t.Errorf("command = %q", got)
	}
}

func TestPlanFix_InitContainerFailure_BarePodQuotesTheError(t *testing.T) {
	prev := "waiting for db...\nfatal: database unreachable\n"
	s := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{Name: "init-container-failure", Namespace: "kubeassist-lab", Phase: "Pending"},
		InitContainers: []model.ContainerInfo{{Name: "wait-for-db", RestartCount: 57,
			State:     model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"},
			LastState: &model.ContainerState{Phase: "terminated", Reason: "Error", ExitCode: ptr32(1)}}},
		Logs:     model.LogBundle{Previous: &prev},
		Detector: model.DetectorResult{FailureType: "InitContainerFailure"},
	}
	plan := PlanFix(s)
	if plan.Facts["exitCode"] != "1" || plan.Facts["lastErrorLogLine"] != "fatal: database unreachable" {
		t.Errorf("facts = %v", plan.Facts)
	}
	if got := commandWith(t, plan, "kubectl get services"); got != "kubectl get services,endpointslices -n kubeassist-lab" {
		t.Errorf("dependency check = %q", got)
	}
	if got := commandWith(t, plan, "kubectl get pod"); got != "kubectl get pod init-container-failure -n kubeassist-lab -o yaml > init-container-failure.yaml" {
		t.Errorf("export = %q", got)
	}
	st := plan.Steps[len(plan.Steps)-1]
	if st.Command != "kubectl replace --force -f init-container-failure.yaml" || !st.IsWriteAction ||
		!strings.Contains(st.Description, `Init container "wait-for-db" keeps exiting with exit code 1; its last error was "fatal: database unreachable"`) {
		t.Errorf("last step = %+v", st)
	}
}

func TestPlanFix_HealthyHasNoPlan(t *testing.T) {
	if p := PlanFix(&model.DiagnosticSnapshot{Detector: model.DetectorResult{FailureType: "Healthy"}}); p != nil {
		t.Errorf("healthy pods need no fix, got %+v", p)
	}
}

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][3]string{
		"nginx:1.27":                          {"", "nginx", "1.27"},
		"busybox":                             {"", "busybox", "latest"},
		"ghcr.io/acme/app:v1":                 {"ghcr.io", "acme/app", "v1"},
		"localhost:5000/app":                  {"localhost:5000", "app", "latest"},
		"library/nginx@sha256:abc":            {"", "library/nginx", "latest"},
		"registry.example.com:443/team/a:1.0": {"registry.example.com:443", "team/a", "1.0"},
	} {
		h, p, tag := splitImage(in)
		if [3]string{h, p, tag} != want {
			t.Errorf("splitImage(%q) = %q %q %q, want %v", in, h, p, tag, want)
		}
	}
}
