// Package collector gathers evidence about a single pod from the
// Kubernetes API and assembles it into a model.DiagnosticSnapshot. This
// package NEVER classifies failures (that's internal/detect) and NEVER
// talks to the AI service (that's internal/ai) — it has exactly one job:
// turn "a namespace and pod name" into "a fully populated snapshot".
//
// WHY SEPARATE COLLECTION FROM DETECTION?
// This is the Single Responsibility Principle applied concretely: if
// detection logic lived inside collection code, you could not unit-test
// detection without a live (or fake) Kubernetes API — you'd be forced
// into slow, flaky integration tests for logic that's actually pure and
// deterministic. By keeping collector's output (a Snapshot) as detect's
// input, detect's tests become instant, offline, and golden-fixture
// -driven, exactly as Phase 3 of the plan specifies.
package collector

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"golang.org/x/sync/errgroup"

	"github.com/yourname/kubeassist/backend-go/internal/model"
)

// Collector wraps a Kubernetes clientset and knows how to build
// snapshots. We define it as a struct (rather than free functions taking
// a clientset parameter every time) so it can hold shared config like
// timeouts and log-truncation limits — this is Go's version of a
// "service object" / dependency container, a very common pattern once
// a package accumulates more than one piece of shared state.
type Collector struct {
	clientset *kubernetes.Clientset

	// ClusterName and ServerVersion are stamped onto every snapshot this
	// Collector builds (schema-required `cluster.name`/`cluster.serverVersion`).
	// Set once at construction — main.go fetches ServerVersion via
	// clientset.Discovery() a single time at startup, so we don't repeat
	// that API call on every single troubleshoot request.
	ClusterName   string
	ServerVersion string

	// LogTailLines caps how many lines of logs we request per container,
	// per the plan's truncation requirement (Section 5: "log truncation
	// e.g. 200 lines / 32KB"). Capping server-side (via the API's
	// TailLines option) rather than client-side is important: it means
	// we don't pull megabytes of logs over the wire just to throw away
	// all but the last 200 lines.
	LogTailLines int64

	// MaxEvents caps how many events we keep per pod (plan: ~30).
	MaxEvents int
}

// New constructs a Collector with sensible defaults matching the plan.
// clusterName/serverVersion are stamped onto every snapshot's
// schema-required `cluster` object.
func New(clientset *kubernetes.Clientset, clusterName, serverVersion string) *Collector {
	return &Collector{
		clientset:     clientset,
		ClusterName:   clusterName,
		ServerVersion: serverVersion,
		LogTailLines:  200,
		MaxEvents:     30,
	}
}

// CollectCore builds the "pass 1" snapshot: everything that doesn't
// require knowing the failure type first. This matches the sequence
// diagram in the plan: Pod, Events, Owner, and Logs all get fetched
// before any detection happens.
func (c *Collector) CollectCore(ctx context.Context, namespace, podName string) (*model.DiagnosticSnapshot, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Wrapping with %w preserves the original error so callers
			// can still use errors.Is(err, ...) checks if needed, while
			// adding context about WHAT we were trying to do when it
			// failed. This is the standard Go error-wrapping idiom
			// (available since Go 1.13's addition of %w to fmt.Errorf).
			return nil, fmt.Errorf("collector: pod %s/%s not found: %w", namespace, podName, err)
		}
		return nil, fmt.Errorf("collector: getting pod %s/%s: %w", namespace, podName, err)
	}

	// We fetch Events, Owner info, and Logs for EVERY container
	// concurrently using errgroup. errgroup.Group is a small,
	// widely-used extension to Go's standard sync.WaitGroup: it runs
	// goroutines, waits for all of them, and — critically — captures
	// the FIRST non-nil error returned by any of them, and cancels a
	// shared context so the other goroutines can stop early too. This
	// is exactly the "fail fast, don't waste work" behavior you want
	// when several parallel calls share one deadline.
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

	// Logs are fetched per-container, so we build one goroutine PER
	// CONTAINER inside the group — this means a 3-container pod fetches
	// all 3 containers' logs in parallel too, not just in parallel with
	// events/owner.
	// Go maps are NOT safe for concurrent writes — two goroutines
	// writing to the same map simultaneously causes a runtime panic
	// ("concurrent map writes"), not just a data race. A sync.Mutex
	// here is the simplest correct fix: pods typically have only a
	// handful of containers, so lock contention is negligible — this
	// is not a hot path that needs a lock-free structure.
	containerLogs := make(map[string]model.LogBundle)
	var logsMu sync.Mutex
	for _, cs := range pod.Status.ContainerStatuses {
		// Historically (Go < 1.22), `cs` here would be ONE shared
		// variable reused across all loop iterations, and a closure
		// capturing it (like the goroutine below) would see whatever
		// its value was at the time the goroutine actually RAN, not
		// when it was launched — a very common source of subtle bugs
		// where all goroutines ended up processing the last element.
		// Go 1.22 changed `for` loop semantics so each iteration gets
		// its OWN variable, making this shadow-assignment technically
		// unnecessary now. We keep it anyway: it costs nothing, makes
		// the intent unambiguous to any reader regardless of which Go
		// version they're mentally modeling, and protects the code if
		// this ever gets vendored somewhere pinned to an older Go.
		cs := cs
		g.Go(func() error {
			bundle, err := c.collectLogs(gctx, namespace, podName, cs)
			if err != nil {
				// A single container's logs failing to fetch (e.g. it
				// never started) shouldn't abort the whole snapshot —
				// partial evidence is still useful, per the plan's
				// "Partial snapshot still diagnosable" principle. This
				// now uses the schema's UnavailableReason field instead
				// of embedding the error into Current as text — Python
				// (and anyone reading the JSON) can now distinguish
				// "empty logs" from "logs we couldn't fetch, here's why"
				// as two genuinely different states.
				bundle = model.LogBundle{UnavailableReason: err.Error()}
			}
			logsMu.Lock()
			containerLogs[cs.Name] = bundle
			logsMu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("collector: gathering evidence for %s/%s: %w", namespace, podName, err)
	}

	containers := buildContainerInfos(pod, pod.Status.ContainerStatuses)

	// Init containers get a SEPARATE, lighter pass: no logs fetched for
	// every one of them (they run once and exit — most have nothing
	// interesting to say once they've succeeded, and pulling logs for
	// every init container on every request would multiply the number
	// of API calls for no benefit in the common case). If a detector
	// later finds a FAILING init container, the evidence from its
	// waiting/terminated STATE is usually enough — logs can be fetched
	// on demand via the existing GET .../logs?container= endpoint if a
	// human wants to dig deeper.
	initContainers := buildContainerInfos(pod, pod.Status.InitContainerStatuses)

	snapshot := &model.DiagnosticSnapshot{
		SchemaVersion: model.SchemaVersion(),
		Cluster: model.ClusterInfo{
			Name:          c.ClusterName,
			ServerVersion: c.ServerVersion,
		},
		Pod: model.PodInfo{
			Name:        pod.Name,
			Namespace:   pod.Namespace,
			Phase:       string(pod.Status.Phase),
			Reason:      pod.Status.Reason,
			Message:     pod.Status.Message,
			NodeName:    pod.Spec.NodeName,
			Labels:      pod.Labels,
			Annotations: pod.Annotations,
			OwnerRefs:   buildOwnerRefs(pod.OwnerReferences),
		},
		InitContainers: initContainers,
		Containers:     containers,
		Conditions:     buildConditions(pod),
		Events:         events,
		Logs:           mergeLogBundles(containerLogs),
		Owners:         owner,
		CollectedAt:    time.Now().UTC(),
	}

	return snapshot, nil
}

// buildOwnerRefs converts Kubernetes' OwnerReference objects into our
// structured model.OwnerRef — schema 1.1.0 requires kind/name/apiVersion
// as an object, not the bare name strings we used to send.
func buildOwnerRefs(refs []metav1.OwnerReference) []model.OwnerRef {
	if len(refs) == 0 {
		return nil
	}
	result := make([]model.OwnerRef, 0, len(refs))
	for _, r := range refs {
		result = append(result, model.OwnerRef{
			Kind:       r.Kind,
			Name:       r.Name,
			APIVersion: r.APIVersion,
		})
	}
	return result
}

// collectEvents lists Events whose involvedObject matches this pod. The
// FieldSelector here is a server-side filter — Kubernetes' API supports
// filtering List calls by specific indexed fields (not arbitrary ones,
// only a whitelist the API server defines) so we don't have to pull
// every event in the namespace and filter client-side.
func (c *Collector) collectEvents(ctx context.Context, namespace, podName string) ([]model.Event, error) {
	fieldSelector := fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", podName)
	list, err := c.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
		Limit:         int64(c.MaxEvents),
	})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}

	events := make([]model.Event, 0, len(list.Items))
	for _, e := range list.Items {
		events = append(events, model.Event{
			Type:          e.Type,
			Reason:        e.Reason,
			Message:       e.Message,
			Count:         e.Count,
			LastTimestamp: e.LastTimestamp.Time,
		})
		if len(events) >= c.MaxEvents {
			break
		}
	}
	return events, nil
}

// collectOwner walks the pod's OwnerReferences to find its ReplicaSet,
// then that ReplicaSet's OwnerReferences to find the Deployment. This
// two-hop walk is how Kubernetes actually models ownership — a Pod is
// owned by a ReplicaSet, which is owned by a Deployment; there's no
// direct Pod-to-Deployment link in the API.
func (c *Collector) collectOwner(ctx context.Context, namespace string, pod *corev1.Pod) (*model.OwnerInfo, error) {
	var rsName string
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			rsName = ref.Name
			break
		}
	}
	if rsName == "" {
		return nil, nil // not managed by a ReplicaSet (e.g. a bare Pod) — not an error
	}

	rs, err := c.clientset.AppsV1().ReplicaSets(namespace).Get(ctx, rsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &model.OwnerInfo{ReplicaSet: &model.ReplicaSetRef{Name: rsName}}, nil
		}
		return nil, fmt.Errorf("getting replicaset %s: %w", rsName, err)
	}

	info := &model.OwnerInfo{
		ReplicaSet: &model.ReplicaSetRef{
			Name:            rs.Name,
			DesiredReplicas: derefInt32(rs.Spec.Replicas, 0),
		},
	}

	for _, ref := range rs.OwnerReferences {
		if ref.Kind == "Deployment" {
			dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err == nil {
				info.Deployment = &model.DeploymentRef{
					Name:     dep.Name,
					Strategy: string(dep.Spec.Strategy.Type),
				}
			}
			break
		}
	}

	return info, nil
}

// collectLogs fetches current (and previous, if the container has
// restarted) logs for one container, truncated server-side via
// TailLines. It now also computes Truncated (did we likely hit the
// TailLines cap, meaning there's more history we didn't fetch?) and
// leaves UnavailableReason empty on success — the caller (CollectCore)
// fills UnavailableReason in in the error case instead of the old hack
// of embedding "(logs unavailable: ...)" directly into the Current
// string, which would have broken schema-required Logs.Current typing
// expectations by conflating "empty logs" with "logs we couldn't fetch,
// here's why" — two genuinely different, now separately representable
// states.
func (c *Collector) collectLogs(ctx context.Context, namespace, podName string, cs corev1.ContainerStatus) (model.LogBundle, error) {
	current, err := c.fetchLogs(ctx, namespace, podName, cs.Name, false)
	if err != nil {
		return model.LogBundle{}, err
	}

	bundle := model.LogBundle{
		Current:   current,
		Truncated: isLikelyTruncated(current, c.LogTailLines),
	}

	// Only attempt "previous" logs if the container has actually
	// restarted — requesting previous logs on a container that's never
	// restarted returns a guaranteed error from the API, so checking
	// RestartCount first avoids a pointless failing call.
	if cs.RestartCount > 0 {
		previous, err := c.fetchLogs(ctx, namespace, podName, cs.Name, true)
		if err == nil {
			bundle.Previous = &previous
		}
		// If it errors anyway (e.g. previous container's logs were
		// already garbage collected by the kubelet), we simply leave
		// Previous nil (serializes as JSON null, per the schema) rather
		// than failing the whole snapshot.
	}

	return bundle, nil
}

// isLikelyTruncated is a heuristic, not a certainty: if the number of
// lines we got back equals exactly the TailLines cap we requested, it's
// likely (though not guaranteed — the container could coincidentally
// have emitted EXACTLY that many lines total) that older history exists
// beyond what we fetched. Kubernetes' log API doesn't tell us the total
// line count directly, so this is the best signal available without a
// second, more expensive call.
func isLikelyTruncated(logText string, tailLines int64) bool {
	if logText == "" {
		return false
	}
	lineCount := int64(strings.Count(logText, "\n"))
	if !strings.HasSuffix(logText, "\n") && logText != "" {
		lineCount++ // count a trailing partial line without a final newline
	}
	return lineCount >= tailLines
}

func (c *Collector) fetchLogs(ctx context.Context, namespace, podName, containerName string, previous bool) (string, error) {
	req := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &c.LogTailLines,
	})

	// req.Stream returns an io.ReadCloser — logs come back as a plain
	// HTTP response body stream, not a typed object, because logs are
	// unstructured text, not a Kubernetes API resource. This is the same
	// mechanism `kubectl logs` uses under the hood.
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("streaming logs for container %s: %w", containerName, err)
	}
	defer stream.Close() // ALWAYS close response bodies — leaking these leaks TCP connections

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("reading log stream for container %s: %w", containerName, err)
	}
	return string(data), nil
}

// buildContainerInfos converts Kubernetes' container status list into
// our model.ContainerInfo shape. It accepts an explicit `statuses` slice
// (rather than always reading pod.Status.ContainerStatuses internally)
// specifically so it can ALSO be used for pod.Status.InitContainerStatuses
// — the two are structurally identical (both []corev1.ContainerStatus)
// but Kubernetes exposes them as two separate fields on Pod.Status, since
// init containers and regular containers have fundamentally different
// lifecycles (init containers run once, in order, to completion before
// any regular container starts).
//
// specByName is looked up from pod.Spec.Containers OR
// pod.Spec.InitContainers depending on which status list was passed —
// see the two call sites in CollectCore.
func buildContainerInfos(pod *corev1.Pod, statuses []corev1.ContainerStatus) []model.ContainerInfo {
	allSpecs := append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...)
	specByName := make(map[string]corev1.Container, len(allSpecs))
	for _, c := range allSpecs {
		specByName[c.Name] = c
	}

	result := make([]model.ContainerInfo, 0, len(statuses))
	for _, cs := range statuses {
		spec := specByName[cs.Name]
		info := model.ContainerInfo{
			Name:         cs.Name,
			Image:        cs.Image,
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

func toContainerState(s corev1.ContainerState) model.ContainerState {
	switch {
	case s.Waiting != nil:
		return model.ContainerState{Phase: "waiting", Reason: s.Waiting.Reason, Message: s.Waiting.Message}
	case s.Running != nil:
		return model.ContainerState{Phase: "running"}
	case s.Terminated != nil:
		code := s.Terminated.ExitCode
		return model.ContainerState{
			Phase:    "terminated",
			Reason:   s.Terminated.Reason,
			Message:  s.Terminated.Message,
			ExitCode: &code,
		}
	default:
		return model.ContainerState{Phase: "unknown"}
	}
}

func isZeroContainerState(s corev1.ContainerState) bool {
	return s.Waiting == nil && s.Running == nil && s.Terminated == nil
}

// toResourceSummary matches the new schema shape: a distinguished
// LimitsMemory (used directly by detectOOMKilled's evidence text)
// alongside generic Requests/Limits maps keyed by Kubernetes resource
// name ("cpu", "memory", "ephemeral-storage", ...). We only include
// resources the container SPEC actually declares — corev1's
// ResourceList only contains keys that were set, so ranging over it
// naturally skips anything unspecified rather than emitting zero-value
// "0" entries for every possible resource type.
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

func toProbeSummary(c corev1.Container) model.ProbeSummary {
	summarizeProbe := func(p *corev1.Probe) string {
		if p == nil {
			return ""
		}
		switch {
		case p.HTTPGet != nil:
			return fmt.Sprintf("HTTP GET %s:%s", p.HTTPGet.Path, p.HTTPGet.Port.String())
		case p.TCPSocket != nil:
			return fmt.Sprintf("TCP %s", p.TCPSocket.Port.String())
		case p.Exec != nil:
			return "exec probe"
		default:
			return "configured"
		}
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

// mergeLogBundles picks a "primary" log bundle for the top-level
// snapshot.Logs field. With multiple containers, this is a simplification
// — we take the first container's logs for the top-level summary, while
// the FULL per-container detail lives in Containers[].State. A future
// iteration could make Logs a map instead; we're keeping the MVP
// contract matching the plan's simpler `logs: { current, previous }`
// shape for now (documented here so it's a visible decision, not an
// accident).
func mergeLogBundles(logs map[string]model.LogBundle) model.LogBundle {
	for _, bundle := range logs {
		return bundle
	}
	return model.LogBundle{}
}

func derefInt32(p *int32, fallback int32) int32 {
	if p == nil {
		return fallback
	}
	return *p
}
