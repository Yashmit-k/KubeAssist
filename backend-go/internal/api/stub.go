package api

import (
	"fmt"
	"strings"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// playbookEntry is a deterministic, evidence-free remediation template
// for one failure type. It is what the backend returns when the AI
// service is not configured or is unreachable ("detector_only_stub"
// mode): the classification and evidence come from the detector, and
// these generic steps make the answer actionable without an LLM.
type playbookEntry struct {
	summary   string
	rootCause string
	steps     []stubStep
	verify    []string
	rollback  string
	docs      []model.DocumentationRef
}

type stubStep struct {
	desc  string
	cmd   string // may contain {pod} {ns} {owner} {container}
	write bool
}

var (
	docDebugPods  = model.DocumentationRef{Title: "Debug Pods", URL: "https://kubernetes.io/docs/tasks/debug/debug-application/debug-pods/"}
	docDebugRun   = model.DocumentationRef{Title: "Debug Running Pods", URL: "https://kubernetes.io/docs/tasks/debug/debug-application/debug-running-pod/"}
	docImages     = model.DocumentationRef{Title: "Images", URL: "https://kubernetes.io/docs/concepts/containers/images/"}
	docMemory     = model.DocumentationRef{Title: "Assign Memory Resources to Containers and Pods", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/assign-memory-resource/"}
	docProbes     = model.DocumentationRef{Title: "Configure Liveness, Readiness and Startup Probes", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/"}
	docConfigMap  = model.DocumentationRef{Title: "Configure a Pod to Use a ConfigMap", URL: "https://kubernetes.io/docs/tasks/configure-pod-container/configure-pod-configmap/"}
	docScheduling = model.DocumentationRef{Title: "Kubernetes Scheduler", URL: "https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/"}
	docServices   = model.DocumentationRef{Title: "Debug Services", URL: "https://kubernetes.io/docs/tasks/debug/debug-application/debug-service/"}
	docVolumes    = model.DocumentationRef{Title: "Volumes", URL: "https://kubernetes.io/docs/concepts/storage/volumes/"}
	docInit       = model.DocumentationRef{Title: "Init Containers", URL: "https://kubernetes.io/docs/concepts/workloads/pods/init-containers/"}
	docEviction   = model.DocumentationRef{Title: "Node-pressure Eviction", URL: "https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/"}
)

var playbook = map[string]playbookEntry{
	"CrashLoopBackOff": {
		summary:   "The container keeps exiting and Kubernetes is restarting it with increasing back-off.",
		rootCause: "The application process exits shortly after starting (bad command, missing configuration, or an unhandled startup error).",
		steps: []stubStep{
			{"Read the logs of the previous (crashed) container instance for the exit reason.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", false},
			{"Check the last termination exit code and reason.", "kubectl get pod {pod} -n {ns} -o jsonpath='{.status.containerStatuses[*].lastState.terminated}'", false},
			{"Fix the command, arguments or configuration in the workload spec and roll out the change.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod -n {ns} -w", "kubectl logs {pod} -n {ns} -c {container} --tail=50"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docDebugRun, docDebugPods},
	},
	"OOMKilled": {
		summary:   "The container was killed for exceeding its memory limit.",
		rootCause: "The process's memory usage exceeded the container's memory limit.",
		steps: []stubStep{
			{"Confirm the memory limit and the OOMKilled termination.", "kubectl describe pod {pod} -n {ns}", false},
			{"Check recent memory usage if metrics-server is installed.", "kubectl top pod {pod} -n {ns} --containers", false},
			{"Raise the memory limit (and request) or reduce the application's memory use.", "kubectl set resources {owner} -n {ns} -c {container} --limits=memory=<new-limit>", true},
		},
		verify:   []string{"kubectl get pod -n {ns} -w", "kubectl describe pod {pod} -n {ns}"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docMemory},
	},
	"ImagePullBackOff": {
		summary:   "The kubelet cannot pull the container image, so the container never starts.",
		rootCause: "The image name or tag is wrong, the registry is unreachable, or pull credentials are missing.",
		steps: []stubStep{
			{"Read the exact pull error in the pod events.", "kubectl describe pod {pod} -n {ns}", false},
			{"Verify the image reference exists (tag spelling, registry host).", "kubectl get pod {pod} -n {ns} -o jsonpath='{.spec.containers[*].image}'", false},
			{"Correct the image reference, or add an imagePullSecret for a private registry.", "kubectl set image {owner} -n {ns} {container}=<registry/image:tag>", true},
		},
		verify:   []string{"kubectl get pod -n {ns} -w"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docImages},
	},
	"InitContainerFailure": {
		summary:   "An init container is failing, so the application containers have not started.",
		rootCause: "The init container's command exits non-zero (often waiting on a dependency that is unavailable) or its image cannot be pulled.",
		steps: []stubStep{
			{"Read the failing init container's logs.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", false},
			{"Inspect init container state and events.", "kubectl describe pod {pod} -n {ns}", false},
			{"Fix the init command, its dependency, or its image in the workload spec.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -w"},
		rollback: "Revert the init container change in your manifests and re-apply.",
		docs:     []model.DocumentationRef{docInit},
	},
	"VolumeMountFailure": {
		summary:   "A volume could not be attached or mounted, so the pod cannot start.",
		rootCause: "A referenced PersistentVolumeClaim, ConfigMap or Secret is missing, unbound, or not attachable on this node.",
		steps: []stubStep{
			{"Read the FailedMount / FailedAttachVolume events.", "kubectl describe pod {pod} -n {ns}", false},
			{"Check that referenced PVCs exist and are Bound.", "kubectl get pvc -n {ns}", false},
			{"Create the missing object or fix the volume reference in the spec.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -w"},
		rollback: "Revert the volume change in your manifests and re-apply.",
		docs:     []model.DocumentationRef{docVolumes},
	},
	"CreateContainerConfigError": {
		summary:   "The container cannot be created because configuration it references is missing.",
		rootCause: "An environment variable references a ConfigMap or Secret (or a key in one) that does not exist.",
		steps: []stubStep{
			{"Read the exact missing reference in the container's waiting message.", "kubectl describe pod {pod} -n {ns}", false},
			{"List the ConfigMaps in the namespace and their keys.", "kubectl get configmaps -n {ns}", false},
			{"Create the missing ConfigMap/Secret or key, or fix the reference.", "kubectl create configmap <name> -n {ns} --from-literal=<key>=<value>", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -w"},
		rollback: "Delete the ConfigMap/key you added if it was not the right fix.",
		docs:     []model.DocumentationRef{docConfigMap},
	},
	"FailedScheduling": {
		summary:   "The scheduler cannot find a node that satisfies the pod's requirements.",
		rootCause: "Resource requests exceed free capacity, or node selectors, affinity rules or taints exclude every node.",
		steps: []stubStep{
			{"Read the FailedScheduling event for the exact predicate that failed.", "kubectl describe pod {pod} -n {ns}", false},
			{"Compare requests with node allocatable capacity and taints.", "kubectl describe nodes", false},
			{"Lower requests, relax selectors/affinity, add tolerations, or add capacity.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -o wide -w"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docScheduling},
	},
	"ProbeFailure": {
		summary:   "The liveness probe is failing, so Kubernetes keeps restarting the container.",
		rootCause: "The probe fires before the app is ready, targets the wrong path/port, or its timeout is too short.",
		steps: []stubStep{
			{"Read the Unhealthy events to see the probe's failure.", "kubectl describe pod {pod} -n {ns}", false},
			{"Compare the probe's initialDelaySeconds with the app's real startup time in the logs.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", false},
			{"Fix the probe path/port, or add a startupProbe / increase delays.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -w"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docProbes},
	},
	"ReadinessFailure": {
		summary:   "The container is running but not Ready, so it receives no Service traffic.",
		rootCause: "The readiness probe fails — usually a dependency the app checks is unavailable, or the probe is misconfigured.",
		steps: []stubStep{
			{"Read the readiness Unhealthy events.", "kubectl describe pod {pod} -n {ns}", false},
			{"Check the app's logs for dependency errors.", "kubectl logs {pod} -n {ns} -c {container} --tail=100", false},
			{"Fix the dependency or the readiness probe configuration.", "kubectl edit {owner} -n {ns}", true},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns} -w", "kubectl get endpointslices -n {ns}"},
		rollback: "kubectl rollout undo {owner} -n {ns}",
		docs:     []model.DocumentationRef{docProbes, docServices},
	},
	"Evicted": {
		summary:   "The kubelet evicted the pod because its node ran low on a resource.",
		rootCause: "Node-level memory, disk or PID pressure — distinct from the container's own memory limit.",
		steps: []stubStep{
			{"Read the eviction message on the pod.", "kubectl describe pod {pod} -n {ns}", false},
			{"Check node conditions for pressure.", "kubectl describe nodes | grep -A5 Conditions", false},
			{"Set accurate resource requests and free node resources; then remove the failed pod.", "kubectl delete pod {pod} -n {ns}", true},
		},
		verify:   []string{"kubectl get pods -n {ns}"},
		rollback: "No rollback needed; evicted pods are replaced by their controller.",
		docs:     []model.DocumentationRef{docEviction},
	},
	"ServiceSelectorMismatch": {
		summary:   "A Service has no ready endpoints because its selector does not match the pod's labels.",
		rootCause: "The Service selector and the pod template labels disagree (typo or a renamed label).",
		steps: []stubStep{
			{"Compare the Service selector with the pod's labels.", "kubectl get pod {pod} -n {ns} --show-labels", false},
			{"Confirm the Service has no endpoints.", "kubectl get endpointslices -n {ns}", false},
			{"Align the Service selector (or the pod labels) so they match.", "kubectl edit service <service-name> -n {ns}", true},
		},
		verify:   []string{"kubectl get endpointslices -n {ns}"},
		rollback: "Revert the Service selector change.",
		docs:     []model.DocumentationRef{docServices},
	},
	"Healthy": {
		summary:   "No failure signature found: the pod is Running and all containers are Ready.",
		rootCause: "No pod-level failure was detected. If traffic still fails, investigate the Service, Ingress or network policy.",
		steps: []stubStep{
			{"Confirm the pod's status.", "kubectl get pod {pod} -n {ns} -o wide", false},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns}"},
		rollback: "No change was recommended.",
		docs:     []model.DocumentationRef{docDebugRun},
	},
	"Unknown": {
		summary:   "No known failure signature matched this pod's evidence.",
		rootCause: "The evidence is insufficient or mixed; no specific failure type is claimed.",
		steps: []stubStep{
			{"Review the pod's events and container states.", "kubectl describe pod {pod} -n {ns}", false},
			{"Review recent logs.", "kubectl logs {pod} -n {ns} --tail=100", false},
		},
		verify:   []string{"kubectl get pod {pod} -n {ns}"},
		rollback: "No change was recommended.",
		docs:     []model.DocumentationRef{docDebugPods},
	},
}

// stubDiagnosis builds the schema-compliant detector-only response.
// Every required array is non-nil: a nil Go slice marshals to null,
// which fails the schema's "type": "array".
func stubDiagnosis(s *model.DiagnosticSnapshot, aiErr string) model.DiagnosisResponse {
	d := s.Detector
	entry, ok := playbook[d.FailureType]
	if !ok {
		entry = playbook["Unknown"]
	}

	vars := strings.NewReplacer(
		"{pod}", s.Pod.Name,
		"{ns}", s.Pod.Namespace,
		"{owner}", ownerRef(s),
		"{container}", focusContainer(s),
	)

	steps := make([]model.ResolutionStep, 0, len(entry.steps))
	for i, st := range entry.steps {
		cmd := vars.Replace(st.cmd)
		steps = append(steps, model.ResolutionStep{Order: i + 1, Description: st.desc, Command: &cmd, IsWriteAction: st.write})
	}
	verify := make([]string, 0, len(entry.verify))
	for _, v := range entry.verify {
		verify = append(verify, vars.Replace(v))
	}

	evidence := append([]string{}, d.Evidence...)
	if len(evidence) == 0 {
		evidence = []string{fmt.Sprintf("Pod phase %s", s.Pod.Phase)}
	}

	notes := []string{}
	if aiErr != "" {
		notes = append(notes, "The AI service could not be reached, so this is the detector-only diagnosis: classification and evidence are deterministic, remediation steps are a generic playbook. Error: "+truncate(aiErr, 200))
	} else {
		notes = append(notes, "Detector-only mode (no AI service configured): classification and evidence are deterministic; remediation steps are a generic playbook for this failure type.")
	}
	if s.Logs.UnavailableReason != "" {
		notes = append(notes, "Logs: "+s.Logs.UnavailableReason)
	}
	notes = append(notes, d.RelatedHints...)

	return model.DiagnosisResponse{
		SchemaVersion:           model.SchemaVersion(),
		FailureType:             d.FailureType,
		IssueSummary:            entry.summary,
		ObservedEvidence:        evidence,
		ProbableRootCause:       entry.rootCause,
		Reasoning:               fmt.Sprintf("The deterministic detector matched the %s signature (confidence %.2f) from the evidence listed above. No language model was involved.", d.FailureType, d.Confidence),
		ConfidenceScore:         d.Confidence,
		ResolutionSteps:         steps,
		VerificationCommands:    verify,
		RollbackGuidance:        vars.Replace(entry.rollback),
		DocumentationReferences: append([]model.DocumentationRef{}, entry.docs...),
		UncertaintyNotes:        notes,
		Mode:                    "detector_only_stub",
	}
}

// ownerRef returns "deployment/<name>" when the pod belongs to a
// Deployment, otherwise the most specific owner, otherwise the pod.
func ownerRef(s *model.DiagnosticSnapshot) string {
	if s.Owners != nil && s.Owners.Deployment != nil {
		return "deployment/" + s.Owners.Deployment.Name
	}
	for _, o := range s.Pod.OwnerRefs {
		return strings.ToLower(o.Kind) + "/" + o.Name
	}
	return "pod/" + s.Pod.Name
}

// focusContainer picks the container the evidence is about: a failing
// init container first, then the least healthy app container.
func focusContainer(s *model.DiagnosticSnapshot) string {
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
