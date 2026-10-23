package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// rolloutObjects builds a Deployment with two revisions: v1 (healthy,
// 3 days ago) and v2 (5 minutes ago) that changed the image, the
// command, an env value, a secret-looking env var and the memory limit.
func rolloutObjects() []runtime.Object {
	labels := map[string]string{"app": "checkout"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "shop", UID: "dep-uid"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}},
	}
	template := func(image, mode, apiKey, mem string, cmd ...string) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: image, Command: cmd,
			Env:       []corev1.EnvVar{{Name: "MODE", Value: mode}, {Name: "API_KEY", Value: apiKey}},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)}},
		}}}}
	}
	rs := func(name, rev string, age time.Duration, t corev1.PodTemplateSpec) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "shop", Labels: labels,
				Annotations:       map[string]string{"deployment.kubernetes.io/revision": rev},
				CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
				OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "checkout", UID: "dep-uid"}},
			},
			Spec: appsv1.ReplicaSetSpec{Template: t},
		}
	}
	v1 := rs("checkout-v1", "1", 72*time.Hour, template("shop/checkout:2.2", "safe", "old-secret", "256Mi", "serve"))
	v2 := rs("checkout-v2", "2", 5*time.Minute, template("shop/checkout:2.3", "fast", "new-secret", "128Mi", "serve", "--fast"))
	other := rs("unrelated", "9", time.Minute, template("x", "", "", "1Mi")) // not owned by checkout
	other.OwnerReferences = nil
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-v2-abcde", Namespace: "shop", Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "checkout-v2"}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "shop/checkout:2.3"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	return []runtime.Object{dep, v1, v2, other, pod}
}

func TestCollectCore_RolloutHistoryAndTemplateDiff(t *testing.T) {
	c := New(fake.NewSimpleClientset(rolloutObjects()...), "kind-test", "v1.31.0")
	snap, err := c.CollectCore(context.Background(), "shop", "checkout-v2-abcde")
	if err != nil {
		t.Fatal(err)
	}
	r := snap.Rollout
	if r == nil || r.Workload != "deployment/checkout" || r.Revision != 2 || !r.Latest || r.PreviousRevision != 1 || r.PreviousCreatedAt == nil {
		t.Fatalf("rollout = %+v", r)
	}
	got := map[string]model.TemplateChange{}
	for _, ch := range r.Changes {
		got[ch.Field+":"+ch.Name] = ch
	}
	if c := got["image:"]; c.From != "shop/checkout:2.2" || c.To != "shop/checkout:2.3" || c.Container != "app" {
		t.Errorf("image change = %+v", c)
	}
	if c := got["command:"]; c.From != "serve" || c.To != "serve --fast" {
		t.Errorf("command change = %+v", c)
	}
	if c := got["env:MODE"]; c.From != "safe" || c.To != "fast" {
		t.Errorf("env change = %+v", c)
	}
	if c := got["resources:"]; !strings.Contains(c.From, "memory=256Mi") || !strings.Contains(c.To, "memory=128Mi") {
		t.Errorf("resources change = %+v", c)
	}
	key := got["env:API_KEY"]
	if strings.Contains(key.From+key.To, "secret") || !strings.Contains(key.To, "value hidden") {
		t.Errorf("sensitive env values must never appear: %+v", key)
	}
	if len(r.Changes) != 5 {
		t.Errorf("want exactly 5 changes, got %+v", r.Changes)
	}
}

// A ReplicaSet reused by a rollback or re-apply keeps its old creation
// time; the rollout time must come from its pods, which are new.
func TestCollectCore_ReusedReplicaSetIsDatedByItsPods(t *testing.T) {
	objs := rolloutObjects()
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.ReplicaSet:
			if v.Name == "checkout-v2" {
				v.CreationTimestamp = metav1.NewTime(time.Now().Add(-72 * time.Hour))
				v.Labels = map[string]string{"app": "checkout", "pod-template-hash": "v2hash"}
			}
		case *corev1.Pod:
			v.Labels = map[string]string{"app": "checkout", "pod-template-hash": "v2hash"}
			v.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute))
		}
	}
	c := New(fake.NewSimpleClientset(objs...), "kind-test", "v1.31.0")
	snap, err := c.CollectCore(context.Background(), "shop", "checkout-v2-abcde")
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(snap.Rollout.RolledOutAt); age > 3*time.Minute || age < time.Minute {
		t.Errorf("rolled out %v ago; want ~2m (from the pod, not the 3-day-old ReplicaSet)", age)
	}
}
