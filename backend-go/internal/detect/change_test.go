package detect

import (
	"strings"
	"testing"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

func rolloutSnap(age time.Duration, latest bool, changes []model.TemplateChange) *model.DiagnosticSnapshot {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	prev := now.Add(-72 * time.Hour)
	return &model.DiagnosticSnapshot{
		Pod:         model.PodInfo{Name: "checkout-v2-abcde", Namespace: "shop"},
		Owners:      &model.OwnerInfo{Deployment: &model.DeploymentRef{Name: "checkout"}},
		CollectedAt: now,
		Containers: []model.ContainerInfo{{Name: "app", RestartCount: 4,
			State: model.ContainerState{Phase: "waiting", Reason: "CrashLoopBackOff"}}},
		Rollout: &model.RolloutInfo{Workload: "deployment/checkout", Revision: 2, RolledOutAt: now.Add(-age),
			Latest: latest, PreviousRevision: 1, PreviousCreatedAt: &prev, Changes: changes},
		Detector: model.DetectorResult{FailureType: "CrashLoopBackOff", Evidence: []string{"Container \"app\" waiting: CrashLoopBackOff"}},
	}
}

var imageChange = []model.TemplateChange{
	{Field: "image", Container: "app", From: "shop/checkout:2.2", To: "shop/checkout:2.3"},
	{Field: "env", Container: "app", Name: "MODE", From: "safe", To: "fast"},
	{Field: "env", Container: "app", Name: "API_KEY", From: "(value hidden)", To: "(changed, value hidden)"},
}

func TestEnrich_RecentRolloutAddsEvidenceAndRollback(t *testing.T) {
	s := rolloutSnap(4*time.Minute, true, imageChange)
	Enrich(s)
	ev := s.Detector.Evidence
	want := `Runs revision 2 of deployment/checkout, rolled out 4m ago; changed from revision 1: image shop/checkout:2.2 → shop/checkout:2.3; env MODE "safe" → "fast"; env API_KEY changed`
	if len(ev) != 2 || ev[1] != want {
		t.Errorf("evidence = %q", ev)
	}
	last := s.Detector.Fix.Steps[len(s.Detector.Fix.Steps)-1]
	if last.Command != "kubectl rollout undo deployment/checkout -n shop --to-revision=1" || !last.IsWriteAction {
		t.Errorf("rollback step = %+v", last)
	}
	if f := s.Detector.Fix.Facts; f["rolloutRevision"] != "2" || f["previousRevision"] != "1" || f["rolledOutAgo"] != "4m" {
		t.Errorf("facts = %v", f)
	}
}

func TestEnrich_NoRollbackWhenNotApplicable(t *testing.T) {
	for name, s := range map[string]*model.DiagnosticSnapshot{
		"old rollout":     rolloutSnap(10*24*time.Hour, true, imageChange),
		"not latest":      rolloutSnap(time.Minute, false, imageChange),
		"nothing changed": rolloutSnap(time.Minute, true, nil),
		"no rollout": func() *model.DiagnosticSnapshot {
			s := rolloutSnap(time.Minute, true, imageChange)
			s.Rollout = nil
			return s
		}(),
		"healthy pod": func() *model.DiagnosticSnapshot {
			s := rolloutSnap(time.Minute, true, imageChange)
			s.Detector.FailureType = "Healthy"
			return s
		}(),
		"first revision": func() *model.DiagnosticSnapshot {
			s := rolloutSnap(time.Minute, true, imageChange)
			s.Rollout.PreviousRevision = 0
			return s
		}(),
	} {
		Enrich(s)
		for _, e := range s.Detector.Evidence {
			if strings.Contains(e, "rolled out") {
				t.Errorf("%s: unexpected rollout evidence %q", name, e)
			}
		}
		if s.Detector.Fix != nil {
			for _, st := range s.Detector.Fix.Steps {
				if strings.Contains(st.Command, "rollout undo") {
					t.Errorf("%s: unexpected rollback", name)
				}
			}
		}
	}
}

func TestChangeSummaryAndDuration(t *testing.T) {
	many := append(append([]model.TemplateChange{}, imageChange...), model.TemplateChange{Field: "livenessProbe", Container: "app"})
	if got := ChangeSummary(many, 2); got != `image shop/checkout:2.2 → shop/checkout:2.3; env MODE "safe" → "fast"; and 2 more` {
		t.Errorf("summary = %q", got)
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "30s", 12 * time.Minute: "12m", 5 * time.Hour: "5h", 72 * time.Hour: "3d"} {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%v) = %q", d, got)
		}
	}
}
