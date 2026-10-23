package detect

import (
	"fmt"
	"strings"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// recentRollout bounds how old a rollout can be and still be offered as
// the likely trigger of a failure.
const recentRollout = 7 * 24 * time.Hour

// Enrich completes a classified snapshot: it plans the evidence-derived
// fix and, when the failing pod runs a recent Deployment revision that
// changed something, adds what changed as evidence and a rollback to the
// previous revision as an alternative fix. Call it once the
// classification is final (after DetectRelated).
func Enrich(s *model.DiagnosticSnapshot) {
	s.Detector.Fix = PlanFix(s)
	r := s.Rollout
	ft := s.Detector.FailureType
	if r == nil || !r.Latest || r.PreviousRevision == 0 || len(r.Changes) == 0 || ft == "Healthy" || ft == "" {
		return
	}
	at := s.CollectedAt
	if at.IsZero() {
		at = time.Now()
	}
	age := at.Sub(r.RolledOutAt)
	if age < 0 {
		age = 0
	}
	if age > recentRollout {
		return
	}

	summary := ChangeSummary(r.Changes, 3)
	line := fmt.Sprintf("Runs revision %d of %s, rolled out %s ago; changed from revision %d: %s", r.Revision, r.Workload, HumanDuration(age), r.PreviousRevision, summary)
	ev := append([]string{}, s.Detector.Evidence...)
	pos := 1
	if len(ev) == 0 {
		pos = 0
	}
	s.Detector.Evidence = append(ev[:pos], append([]string{line}, ev[pos:]...)...)

	if s.Detector.Fix == nil {
		s.Detector.Fix = &model.FixPlan{Facts: map[string]string{}}
	}
	if s.Detector.Fix.Facts == nil {
		s.Detector.Fix.Facts = map[string]string{}
	}
	f := s.Detector.Fix.Facts
	f["rolloutRevision"] = fmt.Sprint(r.Revision)
	f["rolledOutAgo"] = HumanDuration(age)
	f["previousRevision"] = fmt.Sprint(r.PreviousRevision)
	f["recentChanges"] = ChangeSummary(r.Changes, 6)
	s.Detector.Fix.Steps = append(s.Detector.Fix.Steps, model.FixStep{
		Description: fmt.Sprintf("Revision %d (rolled out %s ago) changed %s. If that change caused this, roll back to revision %d — the version that ran before — then fix the change and redeploy:",
			r.Revision, HumanDuration(age), summary, r.PreviousRevision),
		Command:       fmt.Sprintf("kubectl rollout undo %s -n %s --to-revision=%d", r.Workload, s.Pod.Namespace, r.PreviousRevision),
		IsWriteAction: true,
	})
}

// ChangeSummary renders up to n template changes as one short line.
func ChangeSummary(changes []model.TemplateChange, n int) string {
	parts := make([]string, 0, n+1)
	for i, c := range changes {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(changes)-n))
			break
		}
		parts = append(parts, describeChange(c))
	}
	return strings.Join(parts, "; ")
}

func describeChange(c model.TemplateChange) string {
	short := func(v string) string { return truncateFix(v, 60) }
	switch c.Field {
	case "image":
		return fmt.Sprintf("image %s → %s", c.From, c.To)
	case "env":
		switch {
		case c.From == "" || c.From == "(not set)":
			return fmt.Sprintf("env %s added", c.Name)
		case c.To == "(removed)" || c.To == "(not set)":
			return fmt.Sprintf("env %s removed", c.Name)
		case strings.Contains(c.To, "value hidden"):
			return fmt.Sprintf("env %s changed", c.Name)
		}
		return fmt.Sprintf("env %s %q → %q", c.Name, short(c.From), short(c.To))
	case "container":
		return fmt.Sprintf("container %s %s", c.Container, c.To)
	case "volume":
		return fmt.Sprintf("volume %s %s → %s", c.Name, nonEmpty(c.From, "(none)"), nonEmpty(c.To, "(none)"))
	case "serviceAccount":
		return fmt.Sprintf("serviceAccount %s → %s", nonEmpty(c.From, "default"), nonEmpty(c.To, "default"))
	case "resources":
		return fmt.Sprintf("resources %s → %s", nonEmpty(c.From, "(none)"), nonEmpty(c.To, "(none)"))
	case "command", "args":
		return fmt.Sprintf("%s %q → %q", c.Field, short(c.From), short(c.To))
	}
	return c.Field + " changed"
}

// HumanDuration is a compact age: 45s, 12m, 5h, 3d.
func HumanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
