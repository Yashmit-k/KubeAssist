package fixpr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

func lab(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// changedLines returns the lines that differ (same line count assumed).
func changedLines(t *testing.T, before, after []byte) []string {
	t.Helper()
	a, b := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(a) != len(b) {
		t.Fatalf("line count changed: %d -> %d\n%s", len(a), len(b), after)
	}
	var out []string
	for i := range a {
		if a[i] != b[i] {
			out = append(out, strings.TrimSpace(a[i])+" => "+strings.TrimSpace(b[i]))
		}
	}
	return out
}

func snap(ft string, facts map[string]string) *model.DiagnosticSnapshot {
	return &model.DiagnosticSnapshot{
		Pod:      model.PodInfo{Namespace: "kubeassist-lab"},
		Detector: model.DetectorResult{FailureType: ft, Fix: &model.FixPlan{Facts: facts}},
	}
}

func option(t *testing.T, s *model.DiagnosticSnapshot, id string) Option {
	t.Helper()
	for _, o := range Options(s) {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no %q option in %+v", id, Options(s))
	return Option{}
}

func TestRaiseMemory_SplicesOneLine(t *testing.T) {
	src := lab(t, "lab/oom-failure.yaml")
	o := option(t, snap("OOMKilled", map[string]string{"owner": "deployment/oom-failure", "container": "application", "memoryLimit": "32Mi", "suggestedMemoryLimit": "64Mi"}), "raise-memory")
	if o.Target != (Target{Kind: "Deployment", Name: "oom-failure", Namespace: "kubeassist-lab"}) || o.Inputs[0].Default != "64Mi" {
		t.Fatalf("option = %+v", o)
	}
	out, changes, err := o.Apply(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := changedLines(t, src, out); len(diff) != 1 || diff[0] != "memory: 32Mi => memory: 64Mi" {
		t.Errorf("diff = %q", diff)
	}
	if len(changes) != 1 || changes[0] != "container application: resources.limits.memory = 64Mi" {
		t.Errorf("changes = %q", changes)
	}
	if _, _, err := o.Apply(src, map[string]string{"memory": "lots"}); err == nil {
		t.Error("an invalid quantity must be refused")
	}
}

func TestAddConfigKey_TouchesOnlyTheConfigMap(t *testing.T) {
	src := lab(t, "lab/missing-config-app.yaml")
	o := option(t, snap("CreateContainerConfigError", map[string]string{"owner": "deployment/missing-config-app", "container": "application", "configMap": "application-config", "missingKey": "database-url"}), "add-config-key")
	if _, _, err := o.Apply(src, nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("the value is required, got %v", err)
	}
	out, _, err := o.Apply(src, map[string]string{"value": "postgres://db:5432/app"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "database-url: postgres://db:5432/app") {
		t.Errorf("key not added:\n%s", out)
	}
	// Everything from the first "---" on (the Deployment) is untouched.
	i, j := strings.Index(string(src), "\n---"), strings.Index(string(out), "\n---")
	if i < 0 || string(src[i:]) != string(out[j:]) {
		t.Error("the Deployment document must stay byte-for-byte identical")
	}
}

func TestFixProbePath(t *testing.T) {
	src := lab(t, "lab/readiness-failure.yaml")
	o := option(t, snap("ReadinessFailure", map[string]string{"owner": "deployment/readiness-failure", "container": "web", "probe": "readiness",
		"probeError": "Readiness probe failed: HTTP probe failed with statuscode: 404"}), "fix-probe")
	out, _, err := o.Apply(src, map[string]string{"path": "/"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := changedLines(t, src, out); len(diff) != 1 || !strings.HasSuffix(diff[0], "path: /") {
		t.Errorf("diff = %q", diff)
	}
	if _, _, err := o.Apply(src, map[string]string{"path": "no-slash"}); err == nil {
		t.Error("a path without a leading slash must be refused")
	}
}

func TestRevertRollout(t *testing.T) {
	prev := time.Now()
	ro := func(changes ...model.TemplateChange) *model.DiagnosticSnapshot {
		s := snap("CrashLoopBackOff", map[string]string{"owner": "deployment/bad-rollout", "container": "application", "rolloutRevision": "2"})
		s.Rollout = &model.RolloutInfo{Workload: "deployment/bad-rollout", Revision: 2, Latest: true, PreviousRevision: 1, PreviousCreatedAt: &prev, Changes: changes}
		return s
	}
	// The lab's bad rollout also changed the command: not automatic.
	o := option(t, ro(
		model.TemplateChange{Field: "image", Container: "application", From: "busybox:1.36", To: "busybox:1.37"},
		model.TemplateChange{Field: "command", Container: "application", From: "a", To: "b"}), "revert-rollout")
	if !strings.Contains(o.Unavailable, "also changed command") {
		t.Errorf("unavailable = %q", o.Unavailable)
	}

	// Image + env + an added env var: reverted exactly.
	src := lab(t, "lab-rollout/v2.yaml")
	o = option(t, ro(
		model.TemplateChange{Field: "image", Container: "application", From: "busybox:1.36", To: "busybox:1.37"},
		model.TemplateChange{Field: "env", Container: "application", Name: "MODE", From: "safe", To: "fast"}), "revert-rollout")
	out, changes, err := o.Apply(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	diff := changedLines(t, src, out)
	if len(diff) != 2 || diff[0] != "image: busybox:1.37 => image: busybox:1.36" || diff[1] != "value: fast => value: safe" {
		t.Errorf("diff = %q", diff)
	}
	if len(changes) != 2 {
		t.Errorf("changes = %q", changes)
	}

	// Secrets are never reverted blindly.
	o = option(t, ro(model.TemplateChange{Field: "env", Container: "application", Name: "API_KEY", From: "(value hidden)", To: "(changed, value hidden)"}), "revert-rollout")
	if o.Unavailable == "" {
		t.Error("hidden env values must make the revert unavailable")
	}
}

func TestApplyToFile_Matching(t *testing.T) {
	src := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n---\n# keep me\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: prod\nspec:\n  template:\n    spec:\n      containers:\n        - name: web\n          image: \"nginx:1.27\" # pinned\n")
	set := func(d *editDoc) error {
		c, err := container(d.root, "web")
		if err != nil {
			return err
		}
		return d.set(c, "image", "nginx:1.28", false, "image")
	}
	if _, _, err := ApplyToFile(src, Target{Kind: "Deployment", Name: "web", Namespace: "staging"}, set); err != ErrNoMatch {
		t.Errorf("another namespace must not match, got %v", err)
	}
	out, _, err := ApplyToFile(src, Target{Kind: "Deployment", Name: "web", Namespace: "prod"}, set)
	if err != nil {
		t.Fatal(err)
	}
	if diff := changedLines(t, src, out); len(diff) != 1 || diff[0] != `image: "nginx:1.27" # pinned => image: "nginx:1.28" # pinned` {
		t.Errorf("quoting and comment must be kept: %q", diff)
	}
	if _, _, err := ApplyToFile(src, Target{Kind: "Deployment", Name: "web", Namespace: "prod"}, func(d *editDoc) error { return nil }); err == nil {
		t.Error("no change must be an error, not an empty PR")
	}
}

func TestOptions_NoneForBarePodsOrHealthy(t *testing.T) {
	if got := Options(snap("OOMKilled", map[string]string{"owner": "pod/x", "container": "c", "suggestedMemoryLimit": "64Mi"})); len(got) != 0 {
		t.Errorf("bare pod: %+v", got)
	}
	if got := Options(snap("Healthy", map[string]string{"owner": "deployment/x", "container": "c"})); len(got) != 0 {
		t.Errorf("healthy: %+v", got)
	}
}
