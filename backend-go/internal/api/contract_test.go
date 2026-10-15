package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/collector"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/detect"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/redact"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/testfixtures"
)

// The JSON Schemas in /schemas are the wire contract with the Python AI
// service, which rejects anything that doesn't validate. These tests
// validate what Go actually produces against those same files, so a
// drift is caught in Go's CI instead of as a 400 at runtime.

func loadSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening schema (run tests from the monorepo): %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(path, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validateAgainst(t *testing.T, s *jsonschema.Schema, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytesReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(inst); err != nil {
		t.Errorf("schema violation: %v\npayload: %s", err, raw)
	}
}

type podCase struct {
	name string
	pod  func() (string, string, []runtime.Object)
}

func contractCases() []podCase {
	return []podCase{
		{"crashloop", func() (string, string, []runtime.Object) {
			p, objs := testfixtures.CrashLoopPod("shop", "api-1")
			return "shop", "api-1", append(objs, p)
		}},
		{"healthy", func() (string, string, []runtime.Object) {
			return "shop", "ok-1", []runtime.Object{testfixtures.HealthyPod("shop", "ok-1")}
		}},
		{"pending", func() (string, string, []runtime.Object) {
			return "shop", "big-1", []runtime.Object{testfixtures.PendingUnschedulablePod("shop", "big-1")}
		}},
		{"init-failure", func() (string, string, []runtime.Object) {
			return "shop", "init-1", []runtime.Object{testfixtures.InitFailurePod("shop", "init-1")}
		}},
		{"config-error-with-related", func() (string, string, []runtime.Object) {
			p, objs := testfixtures.MissingConfigKeyPod("shop", "web-1")
			return "shop", "web-1", append(objs, p)
		}},
	}
}

func TestContract_SnapshotsMatchSchema(t *testing.T) {
	schema := loadSchema(t, "diagnostic-snapshot.schema.json")
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			ns, name, objs := tc.pod()
			c := collector.New(fake.NewSimpleClientset(objs...), "kind-test", "v1.31.0")
			snap, err := c.CollectCore(context.Background(), ns, name)
			if err != nil {
				t.Fatal(err)
			}
			core := detect.Detect(snap)
			snap.Detector = core
			if needsRelatedCheck(core, nil) {
				rel, err := c.CollectRelated(context.Background(), ns, name, nil)
				if err != nil {
					t.Fatal(err)
				}
				snap.Related = rel
				snap.Detector = detect.DetectRelated(snap, core)
			}
			snap.Detector.Fix = detect.PlanFix(snap)
			snap.UserQuestion = "why is this failing?"
			redact.Snapshot(snap)
			validateAgainst(t, schema, snap)
		})
	}
}

func TestContract_StubDiagnosesMatchSchema(t *testing.T) {
	schema := loadSchema(t, "diagnosis.schema.json")
	for failureType := range playbook {
		t.Run(failureType, func(t *testing.T) {
			snap := &model.DiagnosticSnapshot{
				Pod:      model.PodInfo{Name: "api-1", Namespace: "shop", Phase: "Running"},
				Detector: model.DetectorResult{FailureType: failureType, Confidence: 0.9, Evidence: []string{"x"}, SearchTerms: []string{}},
			}
			validateAgainst(t, schema, stubDiagnosis(snap, ""))
			validateAgainst(t, schema, stubDiagnosis(snap, "connection refused"))
		})
	}
}

// Every failure type the detector can emit must have a playbook entry
// and be in the schema's enum.
func TestContract_PlaybookCoversSchemaEnum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "diagnosis.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			FailureType struct {
				Enum []string `json:"enum"`
			} `json:"failureType"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	enum := map[string]bool{}
	for _, v := range doc.Properties.FailureType.Enum {
		enum[v] = true
		if _, ok := playbook[v]; !ok {
			t.Errorf("schema failure type %q has no detector-only playbook entry", v)
		}
	}
	for k := range playbook {
		if !enum[k] {
			t.Errorf("playbook entry %q is not in the schema enum", k)
		}
	}
}
