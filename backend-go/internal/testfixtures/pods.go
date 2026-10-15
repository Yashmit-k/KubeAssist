// Package testfixtures builds realistic Kubernetes objects for tests
// that run collection against client-go's fake clientset.
package testfixtures

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var created = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

func ptr[T any](v T) *T { return &v }

// CrashLoopPod returns a Deployment-owned pod whose "api" container is
// crash-looping (exit 1) next to a healthy "sidecar", plus its
// ReplicaSet and Deployment.
func CrashLoopPod(ns, name string) (*corev1.Pod, []runtime.Object) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-7d9", Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api", APIVersion: "apps/v1"}},
		},
		Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(1))},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, CreationTimestamp: created,
			Labels:          map[string]string{"app": "api"},
			Annotations:     map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"env":"password=hunter2"}`},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-7d9", APIVersion: "apps/v1"}},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{
				{
					Name: "api", Image: "ghcr.io/acme/api:1.2.3",
					Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")}},
					LivenessProbe: &corev1.Probe{
						ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"cat", "/tmp/healthy"}}},
						InitialDelaySeconds: 5, PeriodSeconds: 10, TimeoutSeconds: 1, FailureThreshold: 3,
					},
				},
				{Name: "sidecar", Image: "envoyproxy/envoy:v1.30"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady"},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "sidecar", Image: "envoyproxy/envoy:v1.30", Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}},
				},
				{
					Name: "api", Image: "ghcr.io/acme/api:1.2.3", RestartCount: 7,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container=api",
					}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1, Reason: "Error", StartedAt: created, FinishedAt: created,
					}},
				},
			},
		},
	}
	return pod, []runtime.Object{dep, rs}
}

// HealthyPod is a Running, Ready, bare pod.
func HealthyPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: created, Labels: map[string]string{"app": "web"}},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "web", Image: "nginx:1.27"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "web", Image: "nginx:1.27", Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}},
			}},
		},
	}
}

// PendingUnschedulablePod has never been scheduled.
func PendingUnschedulablePod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: created},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "big", Image: "busybox:1.36",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("64")}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
				Message: "0/1 nodes are available: 1 Insufficient cpu.",
			}},
		},
	}
}

// InitFailurePod has an init container in CrashLoopBackOff.
func InitFailurePod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: created},
		Spec: corev1.PodSpec{
			NodeName:       "node-1",
			InitContainers: []corev1.Container{{Name: "wait-for-db", Image: "busybox:1.36"}},
			Containers:     []corev1.Container{{Name: "app", Image: "nginx:1.27"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "wait-for-db", Image: "busybox:1.36", RestartCount: 4,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1, Reason: "Error",
				}},
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Image: "nginx:1.27",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
			}},
		},
	}
}

// MissingConfigKeyPod references ConfigMap key DATABASE_URL that doesn't
// exist (LOG_LEVEL does), and is selected by a Service whose selector
// doesn't match it.
func MissingConfigKeyPod(ns, name string) (*corev1.Pod, []runtime.Object) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "web-config", Namespace: ns},
		Data:       map[string]string{"LOG_LEVEL": "info"},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web-frontend"}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: created, Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name: "web", Image: "nginx:1.27",
				Env: []corev1.EnvVar{
					{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"}, Key: "DATABASE_URL",
					}}},
					{Name: "LOG_LEVEL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"}, Key: "LOG_LEVEL",
					}}},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "web", Image: "nginx:1.27",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CreateContainerConfigError",
					Message: `couldn't find key DATABASE_URL in ConfigMap shop/web-config`,
				}},
			}},
		},
	}
	return pod, []runtime.Object{cm, svc}
}
