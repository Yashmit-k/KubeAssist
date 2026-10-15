package detect

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// PlanFix turns the evidence behind a classification into concrete
// remediation: the exact values involved (Facts) and commands built from
// them (Steps). The playbooks in the API and AI service are templates
// per failure type; this is what makes their final step specific to
// THIS pod — "raise the limit from 32Mi to 64Mi", "add key database-url
// to ConfigMap application-config" — instead of "<new-limit>".
//
// Like Detect, it is deterministic and reads only the snapshot. A
// "<...>" placeholder remains only where the evidence cannot know the
// answer (which image tag was intended, what a config value should be),
// and the step's description names exactly what to fill in.
func PlanFix(s *model.DiagnosticSnapshot) *model.FixPlan {
	p := &planner{
		s:     s,
		ns:    s.Pod.Namespace,
		pod:   s.Pod.Name,
		owner: OwnerRef(s),
		ctr:   FocusContainer(s),
		facts: map[string]string{},
	}
	p.facts["owner"] = p.owner
	p.facts["container"] = p.ctr

	switch s.Detector.FailureType {
	case "OOMKilled":
		p.oomKilled()
	case "ImagePullBackOff":
		p.imagePull()
	case "CreateContainerConfigError":
		p.missingConfig()
	case "VolumeMountFailure":
		p.volumeMount()
	case "FailedScheduling":
		p.scheduling()
	case "ProbeFailure":
		p.probe("liveness", "livenessProbe")
	case "ReadinessFailure":
		p.probe("readiness", "readinessProbe")
	case "CrashLoopBackOff", "InitContainerFailure":
		p.processExit()
	case "ServiceSelectorMismatch":
		p.selectorMismatch()
	case "Evicted":
		p.facts["evictionMessage"] = s.Pod.Message
	default:
		return nil // Healthy / Unknown: nothing to fix
	}
	return &model.FixPlan{Facts: p.facts, Steps: p.steps}
}

type planner struct {
	s                   *model.DiagnosticSnapshot
	ns, pod, owner, ctr string
	facts               map[string]string
	steps               []model.FixStep
}

func (p *planner) read(desc, cmd string) {
	p.steps = append(p.steps, model.FixStep{Description: desc, Command: cmd})
}

func (p *planner) write(desc, cmd string) {
	p.steps = append(p.steps, model.FixStep{Description: desc, Command: cmd, IsWriteAction: true})
}

// bare reports whether the pod has no controller. A bare pod's spec is
// mostly immutable, so spec fixes go through its manifest instead of
// set/patch on a workload.
func (p *planner) bare() bool { return strings.HasPrefix(p.owner, "pod/") }

// changeSpec emits a spec change: the given command for a workload, or
// export -> edit -> replace for a bare pod.
func (p *planner) changeSpec(desc, workloadCmd, bareEdit string) {
	if !p.bare() {
		p.write(desc, workloadCmd)
		return
	}
	file := p.pod + ".yaml"
	p.read(fmt.Sprintf("Pod %q has no controller, so its spec can't be patched in place. Save its manifest:", p.pod),
		fmt.Sprintf("kubectl get pod %s -n %s -o yaml > %s", p.pod, p.ns, file))
	p.write(fmt.Sprintf("%s In %s, %s, then recreate the pod:", desc, file, bareEdit),
		fmt.Sprintf("kubectl replace --force -f %s", file))
}

// containerPatch is a strategic-merge patch for one container of the
// owning workload (merged by container name, so other containers and
// fields are untouched).
func (p *planner) containerPatch(fields map[string]any) string {
	c := map[string]any{"name": p.ctr}
	for k, v := range fields {
		c[k] = v
	}
	body := map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{c}}}}}
	return fmt.Sprintf("kubectl patch %s -n %s -p '%s'", p.owner, p.ns, mustJSON(body))
}

func (p *planner) container() *model.ContainerInfo {
	for _, list := range [][]model.ContainerInfo{p.s.Containers, p.s.InitContainers} {
		for i := range list {
			if list[i].Name == p.ctr {
				return &list[i]
			}
		}
	}
	return nil
}

// --- per failure type ---

func (p *planner) oomKilled() {
	c := p.container()
	if c == nil {
		return
	}
	limit := c.Resources.LimitsMemory
	if limit == "" {
		limit = c.Resources.Limits["memory"]
	}
	if limit == "" {
		p.facts["memoryLimit"] = "none"
		p.read(fmt.Sprintf("Container %q has no memory limit, so it was killed by the node running out of memory. Check what else is using the node's memory:", p.ctr),
			fmt.Sprintf("kubectl describe node %s", nonEmpty(p.s.Pod.NodeName, "<node>")))
		return
	}
	p.facts["memoryLimit"] = limit
	q, err := resource.ParseQuantity(limit)
	if err != nil {
		return
	}
	doubled := resource.NewQuantity(q.Value()*2, resource.BinarySI).String()
	p.facts["suggestedMemoryLimit"] = doubled
	desc := fmt.Sprintf("Raise container %q's memory limit from %s to %s (double). If it is OOMKilled again at %s, the app is likely leaking memory: fix that rather than raising the limit further.", p.ctr, limit, doubled, doubled)
	if p.bare() {
		p.changeSpec(desc, "", fmt.Sprintf("set resources.limits.memory of container %q to %s", p.ctr, doubled))
		return
	}
	p.write(desc, fmt.Sprintf("kubectl set resources %s -n %s -c %s --limits=memory=%s", p.owner, p.ns, p.ctr, doubled))
}

func (p *planner) imagePull() {
	c := p.container()
	if c == nil {
		return
	}
	image := c.Image
	p.facts["image"] = image
	msg := c.State.Message
	for _, e := range p.s.Events {
		if e.Reason == "Failed" && strings.Contains(e.Message, "Failed to pull image") {
			msg = e.Message
			break
		}
	}
	host, path, tag := splitImage(image)
	if host != "" {
		p.facts["registry"] = host
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "server misbehaving"):
		p.facts["pullProblem"] = fmt.Sprintf("registry host %q does not resolve", host)
		p.write(fmt.Sprintf("The registry host %q does not exist (DNS lookup failed), so the image reference is wrong. Replace <registry> with the registry that actually hosts %s:%s (for Docker Hub, drop the host part):", host, path, tag),
			fmt.Sprintf("kubectl set image %s -n %s %s=<registry>/%s:%s", p.owner, p.ns, p.ctr, path, tag))
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "denied") || strings.Contains(lower, "403 forbidden"):
		p.facts["pullProblem"] = "the registry requires credentials"
		server := nonEmpty(host, "https://index.docker.io/v1/")
		p.write(fmt.Sprintf("The registry refused the pull without credentials. Create a pull secret with your registry username and access token:"),
			fmt.Sprintf("kubectl create secret docker-registry regcred -n %s --docker-server=%s --docker-username=<username> --docker-password=<access-token>", p.ns, server))
		if !p.bare() {
			p.write("Make the workload use that secret:",
				fmt.Sprintf("kubectl patch %s -n %s -p '%s'", p.owner, p.ns,
					mustJSON(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"imagePullSecrets": []any{map[string]any{"name": "regcred"}}}}}})))
		}
	case strings.Contains(lower, "not found") || strings.Contains(lower, "manifest unknown"):
		p.facts["pullProblem"] = fmt.Sprintf("tag %q of %s does not exist", tag, joinImage(host, path))
		p.read(fmt.Sprintf("Tag %q does not exist. Confirm from your machine which tags exist (requires Docker):", tag),
			fmt.Sprintf("docker manifest inspect %s", image))
		p.write("Point the container at a tag that exists — replace <tag>:",
			fmt.Sprintf("kubectl set image %s -n %s %s=%s:<tag>", p.owner, p.ns, p.ctr, joinImage(host, path)))
	default:
		p.facts["pullProblem"] = truncateFix(msg, 160)
		p.write(fmt.Sprintf("Fix the image reference %q — replace <image> with the image you meant to run:", image),
			fmt.Sprintf("kubectl set image %s -n %s %s=<image>", p.owner, p.ns, p.ctr))
	}
}

var (
	reKeyInConfigMap = regexp.MustCompile(`couldn't find key (\S+) in ConfigMap ([^/\s]+)/(\S+)`)
	reKeyInSecret    = regexp.MustCompile(`couldn't find key (\S+) in Secret ([^/\s]+)/(\S+)`)
	reConfigMapGone  = regexp.MustCompile(`configmap "([^"]+)" not found`)
	reSecretGone     = regexp.MustCompile(`secret "([^"]+)" not found`)
	rePVCGone        = regexp.MustCompile(`persistentvolumeclaim "([^"]+)" not found`)
	reVolumeName     = regexp.MustCompile(`for volume "([^"]+)"`)
)

func (p *planner) missingConfig() {
	msg := ""
	if c := p.container(); c != nil {
		msg = c.State.Message
	}
	for _, e := range p.s.Events {
		if msg == "" && e.Reason == "Failed" {
			msg = e.Message
		}
	}
	if m := reKeyInConfigMap.FindStringSubmatch(msg); m != nil {
		key, name := m[1], m[3]
		p.facts["configMap"], p.facts["missingKey"] = name, key
		p.write(fmt.Sprintf("ConfigMap %q exists but has no key %q, which container %q reads. Add it — replace <value> with what your app expects. The kubelet retries on its own; no restart needed.", name, key, p.ctr),
			fmt.Sprintf("kubectl patch configmap %s -n %s --type merge -p '%s'", name, p.ns, mustJSON(map[string]any{"data": map[string]string{key: "<value>"}})))
		return
	}
	if m := reKeyInSecret.FindStringSubmatch(msg); m != nil {
		key, name := m[1], m[3]
		p.facts["secret"], p.facts["missingKey"] = name, key
		p.write(fmt.Sprintf("Secret %q exists but has no key %q, which container %q reads. Add it — replace <value>. The kubelet retries on its own.", name, key, p.ctr),
			fmt.Sprintf("kubectl patch secret %s -n %s --type merge -p '%s'", name, p.ns, mustJSON(map[string]any{"stringData": map[string]string{key: "<value>"}})))
		return
	}
	if m := reConfigMapGone.FindStringSubmatch(msg); m != nil {
		name := m[1]
		p.facts["missingConfigMap"] = name
		p.write(fmt.Sprintf("ConfigMap %q does not exist. Create it with the keys container %q reads — replace each <value>:", name, p.ctr),
			fmt.Sprintf("kubectl create configmap %s -n %s %s", name, p.ns, literals(p.missingKeys(name))))
		return
	}
	if m := reSecretGone.FindStringSubmatch(msg); m != nil {
		name := m[1]
		p.facts["missingSecret"] = name
		p.write(fmt.Sprintf("Secret %q does not exist. Create it with the keys container %q reads — replace each <value>:", name, p.ctr),
			fmt.Sprintf("kubectl create secret generic %s -n %s %s", name, p.ns, literals(nil)))
	}
}

// missingKeys returns keys pass-2 collection found missing in a
// ConfigMap, so a create command can name them.
func (p *planner) missingKeys(configMap string) []string {
	if p.s.Related == nil {
		return nil
	}
	for _, r := range p.s.Related.ConfigMapRefs {
		if r.Name == configMap {
			return r.KeysMissing
		}
	}
	return nil
}

func (p *planner) volumeMount() {
	for _, e := range p.s.Events {
		if e.Reason != "FailedMount" && e.Reason != "FailedAttachVolume" {
			continue
		}
		if m := reVolumeName.FindStringSubmatch(e.Message); m != nil {
			p.facts["volume"] = m[1]
		}
		vol := nonEmpty(p.facts["volume"], "the volume")
		switch {
		case reConfigMapGone.MatchString(e.Message):
			name := reConfigMapGone.FindStringSubmatch(e.Message)[1]
			p.facts["missingConfigMap"] = name
			p.write(fmt.Sprintf("Volume %q mounts ConfigMap %q, which does not exist. Create it — use --from-file=<path> for file contents, or --from-literal for single values. The kubelet retries the mount on its own.", vol, name),
				fmt.Sprintf("kubectl create configmap %s -n %s --from-file=<path>", name, p.ns))
		case reSecretGone.MatchString(e.Message):
			name := reSecretGone.FindStringSubmatch(e.Message)[1]
			p.facts["missingSecret"] = name
			p.write(fmt.Sprintf("Volume %q mounts Secret %q, which does not exist. Create it — the kubelet retries the mount on its own.", vol, name),
				fmt.Sprintf("kubectl create secret generic %s -n %s --from-file=<path>", name, p.ns))
		case rePVCGone.MatchString(e.Message):
			name := rePVCGone.FindStringSubmatch(e.Message)[1]
			p.facts["missingPersistentVolumeClaim"] = name
			p.read(fmt.Sprintf("Volume %q needs PersistentVolumeClaim %q, which does not exist. See which storage classes you can create it with:", vol, name),
				"kubectl get storageclass")
		default:
			p.facts["mountError"] = truncateFix(e.Message, 200)
			continue
		}
		return
	}
}

func (p *planner) scheduling() {
	msg := ""
	for _, e := range p.s.Events {
		if e.Reason == "FailedScheduling" {
			msg = e.Message
			break
		}
	}
	p.facts["schedulingProblem"] = truncateFix(msg, 240)
	insufficient := strings.Contains(msg, "Insufficient cpu") || strings.Contains(msg, "Insufficient memory")
	if insufficient {
		cpu, mem := "", ""
		if c := p.container(); c != nil {
			cpu, mem = c.Resources.Requests["cpu"], c.Resources.Requests["memory"]
		}
		if cpu != "" {
			p.facts["requestsCpu"] = cpu
		}
		if mem != "" {
			p.facts["requestsMemory"] = mem
		}
		p.read(fmt.Sprintf("No node has room for container %q's requests (cpu=%s, memory=%s). See what each node can offer:", p.ctr, nonEmpty(cpu, "unset"), nonEmpty(mem, "unset")),
			"kubectl get nodes -o custom-columns=NAME:.metadata.name,CPU:.status.allocatable.cpu,MEMORY:.status.allocatable.memory")
		desc := "Lower the requests to what the app really needs and what fits a node from the list above — replace the <...> values. Limits must stay at or above requests."
		if p.bare() {
			p.changeSpec(desc, "", fmt.Sprintf("lower resources.requests of container %q", p.ctr))
		} else {
			p.write(desc, fmt.Sprintf("kubectl set resources %s -n %s -c %s --requests=cpu=<cpu>,memory=<memory> --limits=cpu=<cpu>,memory=<memory>", p.owner, p.ns, p.ctr))
		}
	}
	if strings.Contains(msg, "untolerated taint") {
		p.read("Nodes carry taints this pod does not tolerate. List them:",
			"kubectl get nodes -o custom-columns=NAME:.metadata.name,TAINTS:.spec.taints")
	}
	if strings.Contains(msg, "didn't match Pod's node affinity/selector") {
		p.read("No node has the labels this pod's nodeSelector/affinity requires. Compare the node labels:",
			"kubectl get nodes --show-labels")
	}
}

var reProbeTarget = regexp.MustCompile(`^(HTTP GET|TCP) (\S*?):?(\d+)? `)

func (p *planner) probe(kind, field string) {
	c := p.container()
	if c == nil {
		return
	}
	summary := c.Probes.Liveness
	if kind == "readiness" {
		summary = c.Probes.Readiness
	}
	p.facts["probe"] = kind
	if summary != "" {
		p.facts["probeConfig"] = summary
	}
	errMsg := ""
	for _, e := range p.s.Events {
		if e.Reason == "Unhealthy" && strings.Contains(strings.ToLower(e.Message), kind) {
			errMsg = e.Message
			break
		}
	}
	p.facts["probeError"] = truncateFix(errMsg, 200)

	path, port := "", ""
	httpProbe := strings.HasPrefix(summary, "HTTP GET")
	if m := reProbeTarget.FindStringSubmatch(summary + " "); m != nil {
		path, port = m[2], m[3]
	}
	lower := strings.ToLower(errMsg)
	switch {
	case strings.Contains(lower, "connection refused"):
		// exec runs a process in the container, so it is flagged like a
		// write (the AI service's validator treats it the same way).
		p.write(fmt.Sprintf("Nothing is listening on port %s inside container %q. See which ports the app does listen on (needs netstat in the image):", nonEmpty(port, "<port>"), p.ctr),
			fmt.Sprintf("kubectl exec %s -n %s -c %s -- netstat -tln", p.pod, p.ns, p.ctr))
		target := map[string]any{"port": "<app-port>"}
		key := "tcpSocket"
		if httpProbe {
			key = "httpGet"
			target["path"] = nonEmpty(path, "/")
		}
		p.changeSpec(fmt.Sprintf("Point the %s probe at the port the app listens on — replace <app-port> (a number):", kind),
			strings.Replace(p.containerPatch(map[string]any{field: map[string]any{key: target}}), `"<app-port>"`, "<app-port>", 1),
			fmt.Sprintf("set %s.%s.port of container %q to the app's port", field, key, p.ctr))
	case strings.Contains(lower, "statuscode: 404") && httpProbe:
		p.changeSpec(fmt.Sprintf("The app has no %q endpoint (HTTP 404). Point the %s probe at a path the app serves — replace <health-path>, e.g. /:", path, kind),
			p.containerPatch(map[string]any{field: map[string]any{"httpGet": map[string]any{"path": "<health-path>", "port": portValue(port)}}}),
			fmt.Sprintf("set %s.httpGet.path of container %q to a path the app serves", field, p.ctr))
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		p.changeSpec(fmt.Sprintf("The app answers the %s probe too slowly. Give it more time to start and respond:", kind),
			p.containerPatch(map[string]any{field: map[string]any{"initialDelaySeconds": 30, "timeoutSeconds": 5}}),
			fmt.Sprintf("set %s.initialDelaySeconds to 30 and timeoutSeconds to 5", field))
	}
}

func (p *planner) processExit() {
	c := p.container()
	if c != nil {
		st := c.State
		if c.LastState != nil && c.LastState.Phase == "terminated" {
			st = *c.LastState
		}
		if st.ExitCode != nil {
			p.facts["exitCode"] = fmt.Sprint(*st.ExitCode)
		}
	}
	line := lastErrorLogLine(p.s)
	if line != "" {
		p.facts["lastErrorLogLine"] = line
	}

	// The real fix lives in the app's command, config or a dependency,
	// which the evidence can point at but not write. Say exactly what
	// the evidence shows, and give the edit route that works for this
	// kind of owner.
	if reDependency.MatchString(line) {
		p.read("The log says it can't reach something it depends on. Check that the Services it needs exist and have ready endpoints:",
			fmt.Sprintf("kubectl get services,endpointslices -n %s", p.ns))
	}
	who := fmt.Sprintf("Container %q", p.ctr)
	if p.s.Detector.FailureType == "InitContainerFailure" {
		who = fmt.Sprintf("Init container %q", p.ctr)
	}
	desc := who + " keeps exiting"
	if code := p.facts["exitCode"]; code != "" {
		desc += " with exit code " + code
	}
	if line != "" {
		desc += fmt.Sprintf("; its last error was %q", line)
	}
	desc += ". Fix that cause (command, environment/config, or the dependency it needs)"
	if p.bare() {
		p.changeSpec(desc+".", "", fmt.Sprintf("correct container %q's command, env or image", p.ctr))
		return
	}
	p.write(desc+", then save to roll out the change:", fmt.Sprintf("kubectl edit %s -n %s", p.owner, p.ns))
}

var reDependency = regexp.MustCompile(`(?i)unreachable|connection refused|no such host|timed? ?out|could not connect|can't connect|cannot connect|dial tcp`)

func (p *planner) selectorMismatch() {
	svc, _, _ := mismatchedService(p.s)
	if svc == nil {
		return
	}
	p.facts["service"] = svc.Name
	p.facts["serviceSelector"] = mustJSON(svc.Selector)
	p.facts["podLabels"] = mustJSON(p.s.Pod.Labels)
	sel := map[string]any{}
	matched := false
	for k, v := range svc.Selector {
		lv, ok := p.s.Pod.Labels[k]
		switch {
		case !ok:
			sel[k] = nil // the pod doesn't carry this label: drop it from the selector
		case lv != v:
			sel[k], matched = lv, true
		}
	}
	if !matched && len(sel) == 0 {
		return
	}
	p.write(fmt.Sprintf("Service %q selects %s but the pod is labelled %s. Point the selector at the pod's labels (check that no other workload relied on the old selector):", svc.Name, mustJSON(svc.Selector), mustJSON(p.s.Pod.Labels)),
		fmt.Sprintf("kubectl patch service %s -n %s -p '%s'", svc.Name, p.ns, mustJSON(map[string]any{"spec": map[string]any{"selector": sel}})))
}

// --- helpers ---

// splitImage splits "registry.example.com/team/app:v1" into host
// ("registry.example.com"), path ("team/app") and tag ("v1"). A first
// segment is a registry host only if it looks like one (has a dot or
// port, or is localhost), matching how container runtimes resolve names.
func splitImage(image string) (host, path, tag string) {
	ref := image
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, tag = ref[:i], ref[i+1:]
	}
	if first, rest, ok := strings.Cut(ref, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return first, rest, tag
	}
	return "", ref, tag
}

func joinImage(host, path string) string {
	if host == "" {
		return path
	}
	return host + "/" + path
}

func literals(keys []string) string {
	if len(keys) == 0 {
		return "--from-literal=<key>=<value>"
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("--from-literal=%s=<value>", k))
	}
	return strings.Join(parts, " ")
}

// portValue keeps a numeric port numeric in JSON patches.
func portValue(port string) any {
	var n int
	if _, err := fmt.Sscan(port, &n); err == nil && port != "" {
		return n
	}
	return nonEmpty(port, "<port>")
}

// mustJSON renders compact JSON for shell commands. HTML escaping is
// off so "<value>" placeholders stay readable (json.Marshal would emit
// \u003cvalue\u003e).
func mustJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "{}"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func truncateFix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
