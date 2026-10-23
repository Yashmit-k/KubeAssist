package detect

import (
	"fmt"
	"strings"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// RootCause names the underlying cause behind a classified snapshot, for
// grouping a cluster scan: pods that share a key share one fix. Keys are
// about the broken THING (a ConfigMap key, a registry, a workload's
// memory limit), not the pod, so two Deployments reading the same
// missing ConfigMap key land in one group, while replicas of one
// Deployment always do.
//
// It reads the detector result and its fix plan (run PlanFix first).
func RootCause(s *model.DiagnosticSnapshot) (key, title string) {
	d := s.Detector
	f := map[string]string{}
	if d.Fix != nil && d.Fix.Facts != nil {
		f = d.Fix.Facts
	}
	ns := s.Pod.Namespace
	owner := nonEmpty(f["owner"], OwnerRef(s))
	ctr := nonEmpty(f["container"], FocusContainer(s))

	// Missing configuration, whether read as env or mounted as a volume.
	switch {
	case f["configMap"] != "" && f["missingKey"] != "":
		return "configmap-key:" + ns + "/" + f["configMap"] + "/" + f["missingKey"],
			fmt.Sprintf("ConfigMap %q is missing key %q", f["configMap"], f["missingKey"])
	case f["secret"] != "" && f["missingKey"] != "":
		return "secret-key:" + ns + "/" + f["secret"] + "/" + f["missingKey"],
			fmt.Sprintf("Secret %q is missing key %q", f["secret"], f["missingKey"])
	case f["missingConfigMap"] != "":
		return "configmap:" + ns + "/" + f["missingConfigMap"], fmt.Sprintf("ConfigMap %q does not exist", f["missingConfigMap"])
	case f["missingSecret"] != "":
		return "secret:" + ns + "/" + f["missingSecret"], fmt.Sprintf("Secret %q does not exist", f["missingSecret"])
	case f["missingPersistentVolumeClaim"] != "":
		return "pvc:" + ns + "/" + f["missingPersistentVolumeClaim"],
			fmt.Sprintf("PersistentVolumeClaim %q does not exist", f["missingPersistentVolumeClaim"])
	}

	switch d.FailureType {
	case "ImagePullBackOff":
		problem := f["pullProblem"]
		switch {
		case strings.Contains(problem, "does not resolve"):
			return "registry-dns:" + f["registry"], fmt.Sprintf("Registry host %q does not exist", f["registry"])
		case strings.Contains(problem, "credentials"):
			return "registry-auth:" + ns + "/" + f["registry"], fmt.Sprintf("Registry %q needs pull credentials", nonEmpty(f["registry"], "docker.io"))
		case strings.Contains(problem, "does not exist"):
			return "image-tag:" + f["image"], fmt.Sprintf("Image %q does not exist", f["image"])
		}
		return "image:" + f["image"], fmt.Sprintf("Image %q cannot be pulled", f["image"])
	case "OOMKilled":
		if lim := f["memoryLimit"]; lim != "" && lim != "none" {
			return "oom:" + ns + "/" + owner + "/" + ctr, fmt.Sprintf("%s is OOMKilled: memory limit %s is too low (container %q)", owner, lim, ctr)
		}
		return "oom:" + ns + "/" + owner + "/" + ctr, fmt.Sprintf("%s is OOMKilled (container %q)", owner, ctr)
	case "FailedScheduling":
		title := owner + " doesn't fit on any node"
		if f["requestsCpu"] != "" || f["requestsMemory"] != "" {
			title += fmt.Sprintf(" (requests cpu=%s, memory=%s)", nonEmpty(f["requestsCpu"], "unset"), nonEmpty(f["requestsMemory"], "unset"))
		}
		return "scheduling:" + ns + "/" + owner, title
	case "ProbeFailure", "ReadinessFailure":
		kind := nonEmpty(f["probe"], "liveness")
		title := fmt.Sprintf("%s's %s probe fails", owner, kind)
		if e := probeErrorSummary(f["probeError"]); e != "" {
			title += ": " + e
		}
		return "probe:" + ns + "/" + owner + "/" + ctr + "/" + kind, title
	case "CrashLoopBackOff", "InitContainerFailure":
		what := "keeps crashing"
		if d.FailureType == "InitContainerFailure" {
			what = fmt.Sprintf("is blocked by init container %q", ctr)
		}
		title := owner + " " + what
		if line := f["lastErrorLogLine"]; line != "" {
			title += fmt.Sprintf(": %q", truncateFix(line, 90))
		}
		return "crash:" + ns + "/" + owner + "/" + ctr, title
	case "Evicted":
		node := nonEmpty(s.Pod.NodeName, "a node")
		return "evicted:" + node, fmt.Sprintf("Pods evicted from %s (node resource pressure)", node)
	case "ServiceSelectorMismatch":
		return "selector:" + ns + "/" + f["service"], fmt.Sprintf("Service %q selects no pods", f["service"])
	}
	return "other:" + ns + "/" + owner + "/" + d.FailureType, fmt.Sprintf("%s: %s", owner, d.FailureType)
}

// probeErrorSummary shortens a kubelet probe error to its cause.
func probeErrorSummary(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "connection refused"):
		return "connection refused"
	case strings.Contains(lower, "statuscode:"):
		i := strings.Index(lower, "statuscode:")
		return "HTTP " + strings.TrimSpace(msg[i+len("statuscode:"):])
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "timed out"
	}
	return ""
}
