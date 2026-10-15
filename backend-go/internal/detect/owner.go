package detect

import (
	"strings"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// OwnerRef returns "deployment/<name>" when the pod belongs to a
// Deployment, otherwise the most specific owner, otherwise the pod.
func OwnerRef(s *model.DiagnosticSnapshot) string {
	if s.Owners != nil && s.Owners.Deployment != nil {
		return "deployment/" + s.Owners.Deployment.Name
	}
	for _, o := range s.Pod.OwnerRefs {
		return strings.ToLower(o.Kind) + "/" + o.Name
	}
	return "pod/" + s.Pod.Name
}

// FocusContainer picks the container the evidence is about: a failing
// init container first, then the least healthy app container.
func FocusContainer(s *model.DiagnosticSnapshot) string {
	if s.Detector.FailureType == "InitContainerFailure" {
		for _, c := range s.InitContainers {
			if c.State.Phase != "terminated" || (c.State.ExitCode != nil && *c.State.ExitCode != 0) {
				return c.Name
			}
		}
	}
	best := ""
	var bestRestarts int32 = -1
	for _, c := range s.Containers {
		if !c.Ready && c.RestartCount > bestRestarts {
			best, bestRestarts = c.Name, c.RestartCount
		}
	}
	if best != "" {
		return best
	}
	if len(s.Containers) > 0 {
		return s.Containers[0].Name
	}
	return "<container>"
}
