package collector

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yourname/kubeassist/backend-go/internal/model"
)

// PodSummary is a lighter-weight view of a pod than the full
// DiagnosticSnapshot — used for LIST views (the plan's
// GET /api/pods?namespace= response: "phase, ready, restarts,
// waitingReason"). We don't want to run full log collection for every
// pod just to render a table — that would be needlessly slow and
// expensive against a namespace with many pods.
type PodSummary struct {
	Name          string `json:"name"`
	Namespace     string `json:"namespace"`
	Phase         string `json:"phase"`
	Ready         string `json:"ready"` // e.g. "2/3"
	RestartCount  int32  `json:"restartCount"`
	WaitingReason string `json:"waitingReason,omitempty"`
}

// ListPods returns lightweight summaries, optionally filtered by
// namespace (empty string means all namespaces).
func (c *Collector) ListPods(ctx context.Context, namespace string) ([]PodSummary, error) {
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: listing pods: %w", err)
	}

	summaries := make([]PodSummary, 0, len(list.Items))
	for _, pod := range list.Items {
		summaries = append(summaries, summarizePod(pod))
	}
	return summaries, nil
}

func summarizePod(pod corev1.Pod) PodSummary {
	ready, total := 0, len(pod.Status.ContainerStatuses)
	var restarts int32
	var waitingReason string
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
		if cs.State.Waiting != nil && waitingReason == "" {
			waitingReason = cs.State.Waiting.Reason
		}
	}
	return PodSummary{
		Name:          pod.Name,
		Namespace:     pod.Namespace,
		Phase:         string(pod.Status.Phase),
		Ready:         fmt.Sprintf("%d/%d", ready, total),
		RestartCount:  restarts,
		WaitingReason: waitingReason,
	}
}

// ListNamespaces returns all namespace names in the cluster.
func (c *Collector) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: listing namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	return names, nil
}

// FetchContainerLogs is a thin public wrapper around the private
// fetchLogs used internally by CollectCore — it exists separately
// because the GET /api/pods/{ns}/{name}/logs endpoint needs
// ad-hoc single-container log fetching with query-param-driven
// container/previous selection, independent of building a full
// snapshot.
func (c *Collector) FetchContainerLogs(ctx context.Context, namespace, podName, container string, previous bool) (string, error) {
	if container == "" {
		// If the caller didn't specify a container, default to the
		// pod's first container — mirroring kubectl logs' own default
		// behavior when --container isn't passed.
		pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("collector: getting pod to resolve default container: %w", err)
		}
		if len(pod.Spec.Containers) == 0 {
			return "", fmt.Errorf("collector: pod %s/%s has no containers", namespace, podName)
		}
		container = pod.Spec.Containers[0].Name
	}
	return c.fetchLogs(ctx, namespace, podName, container, previous)
}

// CollectRelated performs "pass 2" collection — additional evidence
// fetched only when Go's OWN core detection result warrants it (the
// "two-pass, not always-fetch" principle). Per the confirmed pass-2
// trigger table:
//   - ServiceSelectorMismatch suspected, or an explicit serviceName
//     given -> fetch Services + Endpoints
//   - CreateContainerConfigError suspected -> fetch ConfigMap names/keys
//     referenced by the pod's containers
//
// explicitServiceName, if non-nil, restricts the Services lookup to
// exactly that one Service (fetched directly via Get rather than
// scanning every Service in the namespace via List) — this is the
// caller-supplied serviceName path from TroubleshootRequest, not
// anything Python requests mid-conversation; there is no such
// handshake in this protocol.
//
// This refetches the Pod itself (one extra Get call) rather than taking
// pre-extracted labels as a parameter, because ConfigMap reference
// scanning needs the full container spec (env/envFrom), not just
// labels — keeping one signature that can serve both trigger reasons is
// simpler than two separate methods with overlapping setup.
func (c *Collector) CollectRelated(ctx context.Context, namespace, podName string, explicitServiceName *string) (*model.RelatedInfo, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: getting pod %s/%s for related-info pass: %w", namespace, podName, err)
	}

	related := &model.RelatedInfo{}

	services, err := c.collectRelatedServices(ctx, namespace, explicitServiceName)
	if err != nil {
		return nil, err
	}
	related.Services = services

	related.ConfigMapRefs = c.collectConfigMapRefs(ctx, namespace, pod)

	return related, nil
}

// collectRelatedServices fetches either one specific Service (when
// explicitServiceName is set) or scans every Service in the namespace
// (the fallback when the caller didn't point at one directly).
func (c *Collector) collectRelatedServices(ctx context.Context, namespace string, explicitServiceName *string) ([]model.ServiceSummary, error) {
	if explicitServiceName != nil && *explicitServiceName != "" {
		svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, *explicitServiceName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("collector: getting service %q: %w", *explicitServiceName, err)
		}
		return []model.ServiceSummary{c.toServiceSummary(ctx, namespace, svc)}, nil
	}

	svcList, err := c.clientset.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: listing services: %w", err)
	}

	var services []model.ServiceSummary
	for _, svc := range svcList.Items {
		if len(svc.Spec.Selector) == 0 {
			continue // headless/externalName services with no selector aren't relevant here
		}
		services = append(services, c.toServiceSummary(ctx, namespace, &svc))
	}
	return services, nil
}

func (c *Collector) toServiceSummary(ctx context.Context, namespace string, svc *corev1.Service) model.ServiceSummary {
	readyCount := 0
	endpoints, err := c.clientset.CoreV1().Endpoints(namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err == nil {
		for _, subset := range endpoints.Subsets {
			readyCount += len(subset.Addresses)
		}
	}
	return model.ServiceSummary{
		Name:           svc.Name,
		Selector:       svc.Spec.Selector,
		EndpointsReady: readyCount,
	}
}

// collectConfigMapRefs scans every container (regular + init) for
// ConfigMap references and checks key presence, for
// CreateContainerConfigError diagnosis. Two distinct reference shapes
// exist in the Kubernetes API and are handled differently:
//   - env[].valueFrom.configMapKeyRef: references ONE specific key —
//     we can check whether THAT key exists in the ConfigMap's Data.
//   - envFrom[].configMapRef: loads the WHOLE ConfigMap as env vars,
//     with no single key named — there's nothing per-key to check, so
//     we only verify the ConfigMap itself exists and list what keys
//     it actually has (useful evidence either way).
//
// Errors fetching an individual ConfigMap are non-fatal here — a
// missing ConfigMap IS the evidence (KeysMissing gets populated
// accordingly), not a reason to fail the whole pass-2 collection.
func (c *Collector) collectConfigMapRefs(ctx context.Context, namespace string, pod *corev1.Pod) []model.ConfigMapRef {
	// requestedKeys maps ConfigMap name -> the specific keys referenced
	// via valueFrom.configMapKeyRef across every container.
	requestedKeys := make(map[string][]string)
	// wholeConfigMaps tracks names referenced via envFrom (whole-map
	// load, no specific keys) so we still report on them even with no
	// requested keys to check.
	wholeConfigMaps := make(map[string]bool)

	scanContainer := func(ctr corev1.Container) {
		for _, env := range ctr.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				ref := env.ValueFrom.ConfigMapKeyRef
				requestedKeys[ref.Name] = append(requestedKeys[ref.Name], ref.Key)
			}
		}
		for _, ef := range ctr.EnvFrom {
			if ef.ConfigMapRef != nil {
				wholeConfigMaps[ef.ConfigMapRef.Name] = true
			}
		}
	}
	for _, ctr := range pod.Spec.Containers {
		scanContainer(ctr)
	}
	for _, ctr := range pod.Spec.InitContainers {
		scanContainer(ctr)
	}

	if len(requestedKeys) == 0 && len(wholeConfigMaps) == 0 {
		return nil
	}

	allNames := make(map[string]bool, len(requestedKeys)+len(wholeConfigMaps))
	for name := range requestedKeys {
		allNames[name] = true
	}
	for name := range wholeConfigMaps {
		allNames[name] = true
	}

	var refs []model.ConfigMapRef
	for name := range allNames {
		cm, err := c.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			// ConfigMap itself doesn't exist — every specifically
			// requested key is therefore missing (there's nowhere for
			// it to have come from).
			refs = append(refs, model.ConfigMapRef{
				Name:        name,
				KeysMissing: requestedKeys[name],
			})
			continue
		}

		var present, missing []string
		for _, key := range requestedKeys[name] {
			if _, ok := cm.Data[key]; ok {
				present = append(present, key)
			} else {
				missing = append(missing, key)
			}
		}
		if wholeConfigMaps[name] && len(requestedKeys[name]) == 0 {
			// Purely an envFrom reference — no specific keys were
			// requested, so report every key the ConfigMap actually has
			// as "present" (there's nothing to compare against for
			// "missing" in this case).
			for key := range cm.Data {
				present = append(present, key)
			}
		}

		refs = append(refs, model.ConfigMapRef{
			Name:        name,
			KeysPresent: present,
			KeysMissing: missing,
		})
	}

	return refs
}
