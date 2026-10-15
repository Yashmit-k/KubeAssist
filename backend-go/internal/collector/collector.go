// Package collector gathers evidence about a single pod from the
// Kubernetes API and assembles it into a model.DiagnosticSnapshot. This
// package NEVER classifies failures (that's internal/detect) and NEVER
// talks to the AI service (that's internal/ai) — it has exactly one job:
// turn "a namespace and pod name" into "a fully populated snapshot".
//
// Keeping collection separate from detection means detect's tests are
// instant, offline and fixture-driven, while this package is tested
// against client-go's fake clientset (see collector_test.go).
package collector

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// Collector wraps a Kubernetes client and knows how to build snapshots.
// It depends on kubernetes.Interface rather than the concrete
// *kubernetes.Clientset so tests can inject client-go's fake clientset.
type Collector struct {
	clientset kubernetes.Interface

	// ClusterName and ServerVersion are stamped onto every snapshot
	// (schema-required `cluster.name` / `cluster.serverVersion`).
	ClusterName   string
	ServerVersion string

	// LogTailLines caps how many lines of logs we request per container.
	// Capping server-side (TailLines) means we never pull megabytes of
	// logs over the wire just to throw most of them away.
	LogTailLines int64

	// MaxLogBytes is a second, byte-based cap applied after fetching —
	// 200 lines of a JSON logger can still be very large.
	MaxLogBytes int

	// MaxEvents caps how many events we keep per pod (schema maxItems: 30).
	MaxEvents int
}

// New constructs a Collector with defaults matching the plan
// (200-line tail, 32KB, 30 events).
func New(clientset kubernetes.Interface, clusterName, serverVersion string) *Collector {
	return &Collector{
		clientset:     clientset,
		ClusterName:   clusterName,
		ServerVersion: serverVersion,
		LogTailLines:  200,
		MaxLogBytes:   32 * 1024,
		MaxEvents:     30,
	}
}

// CollectCore builds the "pass 1" snapshot: everything that doesn't
// require knowing the failure type first — pod, events, owner chain and
// logs. Events, owner and logs are fetched concurrently.
func (c *Collector) CollectCore(ctx context.Context, namespace, podName string) (*model.DiagnosticSnapshot, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("collector: pod %s/%s not found: %w", namespace, podName, err)
		}
		return nil, fmt.Errorf("collector: getting pod %s/%s: %w", namespace, podName, err)
	}

	g, gctx := errgroup.WithContext(ctx)

	var events []model.Event
	g.Go(func() error {
		var err error
		events, err = c.collectEvents(gctx, namespace, podName)
		return err
	})

	var owner *model.OwnerInfo
	g.Go(func() error {
		var err error
		owner, err = c.collectOwner(gctx, namespace, pod)
		return err
	})

	// The schema has ONE logs bundle per snapshot, so rather than fetch
	// every container's logs and then pick one arbitrarily, we decide up
	// front which container is the most diagnostically relevant (a
	// failing init container beats a crash-looping app container beats a
	// healthy one) and fetch only its logs.
	var logs model.LogBundle
	target, unavailable := pickLogTarget(pod)
	if target == nil {
		logs = model.LogBundle{UnavailableReason: unavailable}
	} else {
		g.Go(func() error {
			bundle, err := c.collectLogs(gctx, namespace, podName, *target)
			if err != nil {
				// A log fetch failure is evidence in itself (e.g. the
				// container never started), not a reason to fail the
				// whole troubleshoot request.
				bundle = model.LogBundle{UnavailableReason: summarizeLogError(target.Name, err)}
			}
			logs = bundle
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("collector: gathering evidence for %s/%s: %w", namespace, podName, err)
	}

	return &model.DiagnosticSnapshot{
		SchemaVersion: model.SchemaVersion(),
		Cluster: model.ClusterInfo{
			Name:          c.ClusterName,
			ServerVersion: c.ServerVersion,
		},
		Pod: model.PodInfo{
			Name:        pod.Name,
			Namespace:   pod.Namespace,
			Phase:       nonEmpty(string(pod.Status.Phase), "Unknown"),
			Reason:      pod.Status.Reason,
			Message:     pod.Status.Message,
			NodeName:    pod.Spec.NodeName,
			Labels:      pod.Labels,
			Annotations: copyMap(pod.Annotations),
			OwnerRefs:   buildOwnerRefs(pod.OwnerReferences),
		},
		InitContainers: buildContainerInfos(pod, pod.Status.InitContainerStatuses, true),
		Containers:     buildContainerInfos(pod, pod.Status.ContainerStatuses, false),
		Conditions:     buildConditions(pod),
		Events:         events,
		Logs:           logs,
		Owners:         owner,
		CollectedAt:    time.Now().UTC(),
	}, nil
}

// logTarget identifies which container's logs to fetch.
type logTarget struct {
	Name         string
	RestartCount int32
	Started      bool
}

// pickLogTarget chooses the container whose logs best explain the
// failure, or returns a human-readable reason why no logs can exist yet.
func pickLogTarget(pod *corev1.Pod) (*logTarget, string) {
	hasRun := func(cs corev1.ContainerStatus) bool {
		return cs.State.Running != nil || cs.State.Terminated != nil ||
			cs.LastTerminationState.Terminated != nil || cs.RestartCount > 0
	}
	toTarget := func(cs corev1.ContainerStatus) *logTarget {
		return &logTarget{Name: cs.Name, RestartCount: cs.RestartCount, Started: hasRun(cs)}
	}

	// 1. A failing init container blocks everything after it.
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue
		}
		if hasRun(cs) {
			return toTarget(cs), ""
		}
		break
	}

	// 2. The app container that is unhappy, preferring ones that have
	// actually run (and so have logs).
	var best *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if !hasRun(*cs) {
			continue
		}
		if best == nil || (!cs.Ready && best.Ready) || (cs.Ready == best.Ready && cs.RestartCount > best.RestartCount) {
			best = cs
		}
	}
	if best != nil {
		return toTarget(*best), ""
	}

	// 3. Nothing has run: explain why, so the AI doesn't treat empty
	// logs as "the app printed nothing".
	switch {
	case pod.Status.Phase == corev1.PodPending && pod.Spec.NodeName == "":
		return nil, "pod has not been scheduled to a node yet, so no container has started"
	case len(pod.Status.ContainerStatuses) == 0 && len(pod.Status.InitContainerStatuses) == 0:
		return nil, "no container statuses reported yet"
	}
	for _, cs := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return nil, fmt.Sprintf("container %q has never started (waiting: %s)", cs.Name, cs.State.Waiting.Reason)
		}
	}
	return nil, "no container in this pod has started yet"
}

func summarizeLogError(container string, err error) string {
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Sprintf("could not fetch logs for container %q: %s", container, msg)
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func buildOwnerRefs(refs []metav1.OwnerReference) []model.OwnerRef {
	if len(refs) == 0 {
		return nil
	}
	result := make([]model.OwnerRef, 0, len(refs))
	for _, r := range refs {
		result = append(result, model.OwnerRef{Kind: r.Kind, Name: r.Name, APIVersion: r.APIVersion})
	}
	return result
}

// collectEvents fetches events for this pod, newest first, capped at
// MaxEvents. We list all matching events and sort locally: the API's
// Limit option returns an arbitrary page, not the newest ones.
func (c *Collector) collectEvents(ctx context.Context, namespace, podName string) ([]model.Event, error) {
	fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", podName)
	list, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: fieldSelector})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}

	events := make([]model.Event, 0, len(list.Items))
	for _, e := range list.Items {
		// Newer components populate EventTime/Series instead of the
		// legacy LastTimestamp; fall back so sorting stays meaningful.
		ts := e.LastTimestamp.Time
		if ts.IsZero() {
			ts = e.EventTime.Time
		}
		if ts.IsZero() {
			ts = e.CreationTimestamp.Time
		}
		count := e.Count
		if count == 0 && e.Series != nil {
			count = e.Series.Count
		}
		events = append(events, model.Event{
			Type:          e.Type,
			Reason:        e.Reason,
			Message:       e.Message,
			Count:         count,
			LastTimestamp: ts.UTC(),
		})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].LastTimestamp.After(events[j].LastTimestamp) })
	if len(events) > c.MaxEvents {
		events = events[:c.MaxEvents]
	}
	return events, nil
}

// collectOwner walks Pod -> ReplicaSet -> Deployment. Bare pods (no
// ReplicaSet owner) return nil — not an error.
func (c *Collector) collectOwner(ctx context.Context, namespace string, pod *corev1.Pod) (*model.OwnerInfo, error) {
	var rsName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		return nil, nil
	}

	rs, err := c.clientset.AppsV1().ReplicaSets(namespace).Get(ctx, rsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return &model.OwnerInfo{ReplicaSet: &model.ReplicaSetRef{Name: rsName}}, nil
		}
		return nil, fmt.Errorf("getting replicaset %s: %w", rsName, err)
	}

	info := &model.OwnerInfo{
		ReplicaSet: &model.ReplicaSetRef{Name: rs.Name, DesiredReplicas: derefInt32(rs.Spec.Replicas, 0)},
	}
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err == nil {
				info.Deployment = &model.DeploymentRef{Name: dep.Name, Strategy: string(dep.Spec.Strategy.Type)}
			} else {
				info.Deployment = &model.DeploymentRef{Name: ref.Name}
			}
			break
		}
	}
	return info, nil
}

// collectLogs fetches current (and, after a restart, previous) logs for
// one container.
func (c *Collector) collectLogs(ctx context.Context, namespace, podName string, t logTarget) (model.LogBundle, error) {
	bundle := model.LogBundle{}

	current, currentErr := c.fetchLogs(ctx, namespace, podName, t.Name, false)
	if currentErr == nil {
		bundle.Current, bundle.Truncated = c.capLogs(current)
	}

	// Previous logs exist only if the container has restarted — and for a
	// crash-looping container they're usually MORE useful than current
	// ones, since the current instance may not have logged anything yet.
	if t.RestartCount > 0 {
		previous, err := c.fetchLogs(ctx, namespace, podName, t.Name, true)
		if err == nil {
			capped, truncated := c.capLogs(previous)
			bundle.Previous = &capped
			bundle.Truncated = bundle.Truncated || truncated
		}
	}

	if currentErr != nil {
		if bundle.Previous == nil {
			return model.LogBundle{}, currentErr
		}
		bundle.UnavailableReason = summarizeLogError(t.Name, currentErr) + " (previous instance logs included)"
	} else if bundle.Current == "" && bundle.Previous == nil {
		bundle.UnavailableReason = fmt.Sprintf("container %q produced no log output", t.Name)
	}
	return bundle, nil
}

// capLogs enforces the byte cap (keeping the TAIL, where the error
// usually is) and reports whether anything was cut.
func (c *Collector) capLogs(text string) (string, bool) {
	truncated := isLikelyTruncated(text, c.LogTailLines)
	if c.MaxLogBytes > 0 && len(text) > c.MaxLogBytes {
		text = text[len(text)-c.MaxLogBytes:]
		if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
			text = text[i+1:] // don't start mid-line
		}
		truncated = true
	}
	return text, truncated
}

// isLikelyTruncated reports whether a log fetch probably hit the
// TailLines cap. If we got back exactly TailLines lines, there were
// likely more; fewer means we got everything.
func isLikelyTruncated(logText string, tailLines int64) bool {
	if logText == "" {
		return false
	}
	lineCount := int64(strings.Count(logText, "\n"))
	if !strings.HasSuffix(logText, "\n") {
		lineCount++
	}
	return lineCount >= tailLines
}

func (c *Collector) fetchLogs(ctx context.Context, namespace, podName, containerName string, previous bool) (string, error) {
	req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &c.LogTailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("streaming logs for container %s: %w", containerName, err)
	}
	defer stream.Close()

	// Hard upper bound on what we read, independent of TailLines, so a
	// pathological single huge line can't exhaust memory.
	data, err := io.ReadAll(io.LimitReader(stream, 4*1024*1024))
	if err != nil {
		return "", fmt.Errorf("reading log stream for container %s: %w", containerName, err)
	}
	return string(data), nil
}

// buildContainerInfos joins container STATUS (runtime state) with
// container SPEC (resources, probes) by name.
func buildContainerInfos(pod *corev1.Pod, statuses []corev1.ContainerStatus, isInit bool) []model.ContainerInfo {
	allSpecs := append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...)
	specByName := make(map[string]corev1.Container, len(allSpecs))
	for _, c := range allSpecs {
		specByName[c.Name] = c
	}

	// A pod that was never scheduled has no statuses at all. Report its
	// containers from the spec (state not reported) so their resource
	// requests — usually why scheduling failed — are part of the evidence.
	if len(statuses) == 0 && pod.Status.Phase == corev1.PodPending && !podScheduled(pod) {
		specs := pod.Spec.Containers
		if isInit {
			specs = pod.Spec.InitContainers
		}
		for _, c := range specs {
			statuses = append(statuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image})
		}
	}

	result := make([]model.ContainerInfo, 0, len(statuses))
	for _, cs := range statuses {
		spec := specByName[cs.Name]
		image := cs.Image
		if image == "" {
			image = spec.Image
		}
		info := model.ContainerInfo{
			Name:         cs.Name,
			Image:        image,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
			State:        toContainerState(cs.State),
			Resources:    toResourceSummary(spec.Resources),
			Probes:       toProbeSummary(spec),
		}
		if !isZeroContainerState(cs.LastTerminationState) {
			last := toContainerState(cs.LastTerminationState)
			info.LastState = &last
		}
		result = append(result, info)
	}
	return result
}

func podScheduled(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// toContainerState flattens Kubernetes' waiting/running/terminated union.
// The schema's phase enum has no "unknown", so an empty state (seen
// briefly while the kubelet is still reporting) maps to waiting with an
// explicit reason rather than producing a schema-invalid snapshot.
func toContainerState(s corev1.ContainerState) model.ContainerState {
	switch {
	case s.Waiting != nil:
		return model.ContainerState{Phase: "waiting", Reason: s.Waiting.Reason, Message: s.Waiting.Message}
	case s.Running != nil:
		return model.ContainerState{Phase: "running", StartedAt: formatTime(s.Running.StartedAt)}
	case s.Terminated != nil:
		code := s.Terminated.ExitCode
		return model.ContainerState{
			Phase:      "terminated",
			Reason:     s.Terminated.Reason,
			Message:    s.Terminated.Message,
			ExitCode:   &code,
			StartedAt:  formatTime(s.Terminated.StartedAt),
			FinishedAt: formatTime(s.Terminated.FinishedAt),
		}
	default:
		return model.ContainerState{Phase: "waiting", Reason: "StateNotReported"}
	}
}

func formatTime(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func isZeroContainerState(s corev1.ContainerState) bool {
	return s.Waiting == nil && s.Running == nil && s.Terminated == nil
}

func toResourceSummary(r corev1.ResourceRequirements) model.ResourceSummary {
	summary := model.ResourceSummary{}
	if len(r.Requests) > 0 {
		summary.Requests = make(map[string]string, len(r.Requests))
		for name, qty := range r.Requests {
			summary.Requests[string(name)] = qty.String()
		}
	}
	if len(r.Limits) > 0 {
		summary.Limits = make(map[string]string, len(r.Limits))
		for name, qty := range r.Limits {
			summary.Limits[string(name)] = qty.String()
		}
		if mem, ok := r.Limits[corev1.ResourceMemory]; ok {
			summary.LimitsMemory = mem.String()
		}
	}
	return summary
}

// toProbeSummary renders each probe as a short human-readable string
// including its timing, which is what ProbeFailure diagnoses hinge on.
func toProbeSummary(c corev1.Container) model.ProbeSummary {
	summarizeProbe := func(p *corev1.Probe) string {
		if p == nil {
			return ""
		}
		var target string
		switch {
		case p.HTTPGet != nil:
			target = fmt.Sprintf("HTTP GET %s:%s", p.HTTPGet.Path, p.HTTPGet.Port.String())
		case p.TCPSocket != nil:
			target = fmt.Sprintf("TCP %s", p.TCPSocket.Port.String())
		case p.GRPC != nil:
			target = fmt.Sprintf("gRPC %d", p.GRPC.Port)
		case p.Exec != nil:
			target = "exec " + strings.Join(p.Exec.Command, " ")
			if len(target) > 80 {
				target = target[:80] + "…"
			}
		default:
			target = "configured"
		}
		return fmt.Sprintf("%s (initialDelay=%ds period=%ds timeout=%ds failureThreshold=%d)",
			target, p.InitialDelaySeconds, p.PeriodSeconds, p.TimeoutSeconds, p.FailureThreshold)
	}
	return model.ProbeSummary{
		Liveness:  summarizeProbe(c.LivenessProbe),
		Readiness: summarizeProbe(c.ReadinessProbe),
		Startup:   summarizeProbe(c.StartupProbe),
	}
}

func buildConditions(pod *corev1.Pod) []model.Condition {
	result := make([]model.Condition, 0, len(pod.Status.Conditions))
	for _, c := range pod.Status.Conditions {
		result = append(result, model.Condition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	return result
}

func derefInt32(p *int32, fallback int32) int32 {
	if p == nil {
		return fallback
	}
	return *p
}
