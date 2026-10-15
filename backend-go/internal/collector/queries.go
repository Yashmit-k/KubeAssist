package collector

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// ListPods returns lightweight summaries, optionally filtered by
// namespace (empty string means all namespaces), sorted so that
// unhealthy pods come first — that's what a troubleshooting UI wants to
// show at the top.
func (c *Collector) ListPods(ctx context.Context, namespace string) ([]model.PodSummary, error) {
	list, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: listing pods: %w", err)
	}

	summaries := make([]model.PodSummary, 0, len(list.Items))
	for _, pod := range list.Items {
		summaries = append(summaries, SummarizePod(pod))
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		hi, hj := isHealthyStatus(summaries[i]), isHealthyStatus(summaries[j])
		if hi != hj {
			return !hi
		}
		if summaries[i].Namespace != summaries[j].Namespace {
			return summaries[i].Namespace < summaries[j].Namespace
		}
		return summaries[i].Name < summaries[j].Name
	})
	return summaries, nil
}

func isHealthyStatus(p model.PodSummary) bool {
	return p.Status == "Running" || p.Status == "Completed" || p.Status == "Succeeded"
}

// SummarizePod builds a PodSummary, including a kubectl-style STATUS
// string: the most specific reason available, with init-container
// problems prefixed "Init:" the way `kubectl get pods` shows them.
func SummarizePod(pod corev1.Pod) model.PodSummary {
	ready, total := 0, len(pod.Spec.Containers)
	var restarts int32
	var waitingReason string
	status := string(pod.Status.Phase)

initLoop:
	for i, cs := range pod.Status.InitContainerStatuses {
		restarts += cs.RestartCount
		switch {
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0:
			continue
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			status = "Init:" + cs.State.Waiting.Reason
			waitingReason = cs.State.Waiting.Reason
		case cs.State.Terminated != nil:
			status = "Init:" + nonEmpty(cs.State.Terminated.Reason, fmt.Sprintf("ExitCode:%d", cs.State.Terminated.ExitCode))
		default:
			status = fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
		}
		break initLoop
	}

	initDone := len(pod.Status.InitContainerStatuses) == 0 || !hasPrefix(status, "Init:")
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
		if !initDone {
			continue
		}
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			if waitingReason == "" {
				waitingReason = cs.State.Waiting.Reason
			}
			status = cs.State.Waiting.Reason
		} else if cs.State.Terminated != nil && status == string(pod.Status.Phase) {
			status = nonEmpty(cs.State.Terminated.Reason, "Terminated")
		}
	}

	if pod.Status.Reason != "" {
		status = pod.Status.Reason // e.g. Evicted
	}
	if pod.DeletionTimestamp != nil {
		status = "Terminating"
	}

	return model.PodSummary{
		Name:          pod.Name,
		Namespace:     pod.Namespace,
		Phase:         string(pod.Status.Phase),
		Status:        status,
		Ready:         fmt.Sprintf("%d/%d", ready, total),
		RestartCount:  restarts,
		WaitingReason: waitingReason,
		NodeName:      pod.Spec.NodeName,
		CreatedAt:     pod.CreationTimestamp.UTC(),
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ListNamespaces returns all namespace names, sorted.
func (c *Collector) ListNamespaces(ctx context.Context) ([]string, error) {
	list, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: listing namespaces: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	sort.Strings(names)
	return names, nil
}

// FetchContainerLogs is the standalone "view logs" endpoint helper,
// separate from the bounded log collection inside CollectCore. An empty
// container name resolves to the pod's first container.
func (c *Collector) FetchContainerLogs(ctx context.Context, namespace, podName, container string, previous bool) (string, error) {
	if container == "" {
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

// CollectRelated is "pass 2" of collection — called only when a detector
// signals it needs more context (ServiceSelectorMismatch suspected,
// CreateContainerConfigError, or an explicit serviceName). It fetches
// Services (with ready endpoint counts) and the ConfigMap keys the pod
// references. Secret values are never read.
func (c *Collector) CollectRelated(ctx context.Context, namespace, podName string, explicitServiceName *string) (*model.RelatedInfo, error) {
	pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("collector: getting pod %s/%s for related-info pass: %w", namespace, podName, err)
	}

	services, err := c.collectRelatedServices(ctx, namespace, explicitServiceName)
	if err != nil {
		return nil, err
	}

	return &model.RelatedInfo{
		Services:      services,
		ConfigMapRefs: c.collectConfigMapRefs(ctx, namespace, pod),
	}, nil
}

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

// toServiceSummary counts ready endpoints via EndpointSlices
// (discovery.k8s.io/v1). The older core/v1 Endpoints API is deprecated
// as of Kubernetes 1.33.
func (c *Collector) toServiceSummary(ctx context.Context, namespace string, svc *corev1.Service) model.ServiceSummary {
	readyCount := 0
	slices, err := c.clientset.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + svc.Name,
	})
	if err == nil {
		for _, slice := range slices.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
					readyCount++
				}
			}
		}
	}
	return model.ServiceSummary{
		Name:           svc.Name,
		Selector:       svc.Spec.Selector,
		EndpointsReady: readyCount,
	}
}

// collectConfigMapRefs finds every ConfigMap the pod references (via env
// valueFrom, envFrom, or volumes) and reports which requested keys exist.
// Only key NAMES are returned — never values.
func (c *Collector) collectConfigMapRefs(ctx context.Context, namespace string, pod *corev1.Pod) []model.ConfigMapRef {
	requestedKeys := make(map[string][]string)
	allNames := make(map[string]bool)

	scanContainer := func(ctr corev1.Container) {
		for _, env := range ctr.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				ref := env.ValueFrom.ConfigMapKeyRef
				requestedKeys[ref.Name] = append(requestedKeys[ref.Name], ref.Key)
				allNames[ref.Name] = true
			}
		}
		for _, ef := range ctr.EnvFrom {
			if ef.ConfigMapRef != nil {
				allNames[ef.ConfigMapRef.Name] = true
			}
		}
	}
	for _, ctr := range pod.Spec.Containers {
		scanContainer(ctr)
	}
	for _, ctr := range pod.Spec.InitContainers {
		scanContainer(ctr)
	}
	for _, vol := range pod.Spec.Volumes {
		if vol.ConfigMap != nil {
			allNames[vol.ConfigMap.Name] = true
			for _, item := range vol.ConfigMap.Items {
				requestedKeys[vol.ConfigMap.Name] = append(requestedKeys[vol.ConfigMap.Name], item.Key)
			}
		}
	}

	if len(allNames) == 0 {
		return nil
	}

	names := make([]string, 0, len(allNames))
	for name := range allNames {
		names = append(names, name)
	}
	sort.Strings(names)

	refs := make([]model.ConfigMapRef, 0, len(names))
	for _, name := range names {
		cm, err := c.clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			missing := requestedKeys[name]
			if len(missing) == 0 {
				// The whole ConfigMap is absent; report it as such so the
				// detector and the LLM both see the concrete gap.
				missing = []string{"(configmap not found)"}
			}
			refs = append(refs, model.ConfigMapRef{Name: name, KeysMissing: missing})
			continue
		}

		var present, missing []string
		for _, key := range requestedKeys[name] {
			_, inData := cm.Data[key]
			_, inBinary := cm.BinaryData[key]
			if inData || inBinary {
				present = append(present, key)
			} else {
				missing = append(missing, key)
			}
		}
		if len(requestedKeys[name]) == 0 {
			for key := range cm.Data {
				present = append(present, key)
			}
			sort.Strings(present)
		}

		refs = append(refs, model.ConfigMapRef{Name: name, KeysPresent: present, KeysMissing: missing})
	}
	return refs
}
