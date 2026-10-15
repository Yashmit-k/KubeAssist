package collector

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/testfixtures"
)

func TestCollectCore_CrashLoopPicksFailingContainerAndPreviousLogs(t *testing.T) {
	pod, objs := testfixtures.CrashLoopPod("shop", "api-7d9f")
	c := New(fake.NewSimpleClientset(append(objs, pod)...), "kind-test", "v1.31.0")

	snap, err := c.CollectCore(context.Background(), "shop", "api-7d9f")
	if err != nil {
		t.Fatalf("CollectCore: %v", err)
	}

	if snap.Cluster.Name != "kind-test" || snap.Cluster.ServerVersion != "v1.31.0" {
		t.Errorf("cluster info not stamped: %+v", snap.Cluster)
	}
	if len(snap.Containers) != 2 {
		t.Fatalf("want 2 containers, got %d", len(snap.Containers))
	}
	// The fake clientset returns "fake logs" for any log request; what we
	// verify is that previous logs were requested for the restarting
	// container (Previous != nil) — i.e. the crash-looping container,
	// not the healthy sidecar, was chosen.
	if snap.Logs.Previous == nil {
		t.Errorf("expected previous logs for the crash-looping container")
	}
	if snap.Logs.UnavailableReason != "" {
		t.Errorf("unexpected unavailableReason %q", snap.Logs.UnavailableReason)
	}
	if snap.Owners == nil || snap.Owners.Deployment == nil || snap.Owners.Deployment.Name != "api" {
		t.Errorf("owner chain not resolved to Deployment: %+v", snap.Owners)
	}
}

func TestCollectCore_EventsNewestFirstAndCapped(t *testing.T) {
	pod, objs := testfixtures.CrashLoopPod("shop", "api-7d9f")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "ev-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Namespace: "shop"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-7d9f", Namespace: "shop"},
			Reason:         "BackOff",
			Message:        "Back-off restarting failed container",
			Type:           "Warning",
			LastTimestamp:  metav1.NewTime(base.Add(time.Duration(i) * time.Minute)),
		})
	}
	c := New(fake.NewSimpleClientset(append(objs, pod)...), "kind-test", "v1.31.0")

	snap, err := c.CollectCore(context.Background(), "shop", "api-7d9f")
	if err != nil {
		t.Fatalf("CollectCore: %v", err)
	}
	if len(snap.Events) != 30 {
		t.Fatalf("want events capped at 30, got %d", len(snap.Events))
	}
	for i := 1; i < len(snap.Events); i++ {
		if snap.Events[i].LastTimestamp.After(snap.Events[i-1].LastTimestamp) {
			t.Fatalf("events not sorted newest-first at index %d", i)
		}
	}
}

func TestCollectCore_PendingPodExplainsMissingLogs(t *testing.T) {
	pod := testfixtures.PendingUnschedulablePod("shop", "big-1")
	c := New(fake.NewSimpleClientset(pod), "kind-test", "v1.31.0")

	snap, err := c.CollectCore(context.Background(), "shop", "big-1")
	if err != nil {
		t.Fatalf("CollectCore: %v", err)
	}
	if snap.Logs.UnavailableReason == "" {
		t.Error("pending pod should explain why logs are unavailable")
	}
	if snap.Containers == nil || snap.Conditions == nil || snap.Events == nil {
		t.Error("required arrays must be non-nil (they marshal to null otherwise)")
	}
}

func TestCollectCore_NotFound(t *testing.T) {
	c := New(fake.NewSimpleClientset(), "kind-test", "v1.31.0")
	if _, err := c.CollectCore(context.Background(), "shop", "nope"); err == nil {
		t.Fatal("expected an error for a missing pod")
	}
}

func TestSummarizePod_Statuses(t *testing.T) {
	crash, _ := testfixtures.CrashLoopPod("shop", "api-7d9f")
	if got := SummarizePod(*crash).Status; got != "CrashLoopBackOff" {
		t.Errorf("crash-loop status = %q", got)
	}
	initFail := testfixtures.InitFailurePod("shop", "init-1")
	if got := SummarizePod(*initFail).Status; got != "Init:CrashLoopBackOff" {
		t.Errorf("init failure status = %q", got)
	}
	pending := testfixtures.PendingUnschedulablePod("shop", "big-1")
	if got := SummarizePod(*pending).Status; got != "Pending" {
		t.Errorf("pending status = %q", got)
	}
}

func TestListPods_UnhealthyFirst(t *testing.T) {
	crash, objs := testfixtures.CrashLoopPod("shop", "zzz-crash")
	healthy := testfixtures.HealthyPod("shop", "aaa-ok")
	c := New(fake.NewSimpleClientset(append(objs, crash, healthy)...), "kind-test", "v1.31.0")

	pods, err := c.ListPods(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 2 || pods[0].Name != "zzz-crash" {
		t.Fatalf("unhealthy pod should sort first: %+v", pods)
	}
}

func TestCollectRelated_ConfigMapKeysAndServices(t *testing.T) {
	pod, objs := testfixtures.MissingConfigKeyPod("shop", "web-1")
	c := New(fake.NewSimpleClientset(append(objs, pod)...), "kind-test", "v1.31.0")

	rel, err := c.CollectRelated(context.Background(), "shop", "web-1", nil)
	if err != nil {
		t.Fatalf("CollectRelated: %v", err)
	}
	if len(rel.ConfigMapRefs) != 1 {
		t.Fatalf("want 1 configmap ref, got %+v", rel.ConfigMapRefs)
	}
	ref := rel.ConfigMapRefs[0]
	if len(ref.KeysMissing) != 1 || ref.KeysMissing[0] != "DATABASE_URL" {
		t.Errorf("missing keys = %v", ref.KeysMissing)
	}
	if len(ref.KeysPresent) != 1 || ref.KeysPresent[0] != "LOG_LEVEL" {
		t.Errorf("present keys = %v", ref.KeysPresent)
	}
	if len(rel.Services) != 1 || rel.Services[0].EndpointsReady != 0 {
		t.Errorf("services = %+v", rel.Services)
	}
}

// A never-scheduled pod has no container statuses; its containers come
// from the spec so the requests that blocked scheduling are evidence.
func TestCollectCore_UnscheduledPodReportsSpecContainers(t *testing.T) {
	pod := testfixtures.PendingUnschedulablePod("shop", "big-1")
	c := New(fake.NewSimpleClientset(pod), "kind-test", "v1.31.0")
	snap, err := c.CollectCore(context.Background(), "shop", "big-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Containers) != 1 {
		t.Fatalf("want the spec container, got %+v", snap.Containers)
	}
	got := snap.Containers[0]
	if got.Name != "big" || got.Ready || got.State.Phase != "waiting" || got.Resources.Requests["cpu"] != "64" {
		t.Errorf("container = %+v", got)
	}
}
