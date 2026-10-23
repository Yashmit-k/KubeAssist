package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
	"github.com/Yashmit-k/KubeAssist/backend-go/internal/testfixtures"
)

func scanCluster(t *testing.T, objs ...runtime.Object) scanResponse {
	t.Helper()
	rec := do(t, newLocalServer(t, objs...).NewRouter(), "POST", "/api/scan", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body)
	}
	var out scanResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestScan_GroupsPodsByRootCause(t *testing.T) {
	// Two different pods read the same missing ConfigMap key: one cause.
	webA, objs := testfixtures.MissingConfigKeyPod("shop", "web-a")
	webB, _ := testfixtures.MissingConfigKeyPod("shop", "web-b")
	crash, crashObjs := testfixtures.CrashLoopPod("shop", "api-1")
	// The unschedulable pod lives in its own namespace: the fake
	// clientset ignores the collector's per-pod event field selector.
	schedEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "big-1.sched", Namespace: "batch"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "big-1", Namespace: "batch"},
		Type:           "Warning", Reason: "FailedScheduling", Message: "0/1 nodes are available: 1 Insufficient cpu.",
	}
	all := append(append(objs, crashObjs...), webA, webB, crash, schedEvent,
		testfixtures.HealthyPod("shop", "ok-1"), testfixtures.PendingUnschedulablePod("batch", "big-1"))

	out := scanCluster(t, all...)
	if out.TotalPods != 5 || out.Checked != 4 || out.Healthy != 0 || len(out.Errors) != 0 {
		t.Fatalf("counts: total=%d checked=%d healthy=%d errors=%v", out.TotalPods, out.Checked, out.Healthy, out.Errors)
	}
	if len(out.Groups) != 3 {
		t.Fatalf("want 3 root causes, got %d: %+v", len(out.Groups), out.Groups)
	}
	top := out.Groups[0] // biggest blast radius first
	if top.Title != `ConfigMap "web-config" is missing key "DATABASE_URL"` || len(top.Pods) != 2 ||
		top.Pods[0].Name != "web-a" || top.Pods[1].Name != "web-b" {
		t.Errorf("top group = %+v", top)
	}
	if top.Fix == nil || !strings.Contains(writeCmd(top.Fix), `kubectl patch configmap web-config -n shop --type merge -p '{"data":{"DATABASE_URL":"<value>"}}'`) {
		t.Errorf("group fix = %+v", top.Fix)
	}
	types := map[string]bool{}
	for _, g := range out.Groups {
		types[g.FailureType] = true
	}
	if !types["CrashLoopBackOff"] || !types["FailedScheduling"] {
		t.Errorf("failure types = %v", types)
	}
}

func TestScan_HealthyClusterAndNeedsScan(t *testing.T) {
	out := scanCluster(t, testfixtures.HealthyPod("shop", "ok-1"))
	if out.TotalPods != 1 || out.Checked != 0 || len(out.Groups) != 0 {
		t.Errorf("a healthy cluster needs no collection: %+v", out)
	}
	for _, tc := range []struct {
		p    model.PodSummary
		want bool
	}{
		{model.PodSummary{Status: "Running", Ready: "1/1"}, false},
		{model.PodSummary{Status: "Completed", Ready: "0/1"}, false},
		{model.PodSummary{Status: "Running", Ready: "0/1"}, true},
		{model.PodSummary{Status: "Running", Ready: "1/1", RestartCount: 3}, true},
		{model.PodSummary{Status: "CrashLoopBackOff", Ready: "0/1"}, true},
	} {
		if got := needsScan(tc.p); got != tc.want {
			t.Errorf("needsScan(%+v) = %v", tc.p, got)
		}
	}
}

func writeCmd(f *model.FixPlan) string {
	var cmds []string
	for _, st := range f.Steps {
		if st.IsWriteAction {
			cmds = append(cmds, st.Command)
		}
	}
	return strings.Join(cmds, "\n")
}

func verify(t *testing.T, objs []runtime.Object, pod, body string) verifyResponse {
	t.Helper()
	rec := do(t, newLocalServer(t, objs...).NewRouter(), "POST", "/api/pods/shop/"+pod+"/verify", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body)
	}
	var out verifyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerify_Verdicts(t *testing.T) {
	crash, crashObjs := testfixtures.CrashLoopPod("shop", "api-1")
	objs := append(crashObjs, crash)

	if out := verify(t, objs, "api-1", `{"failureType":"CrashLoopBackOff"}`); out.Verdict != verdictStill || out.Fix == nil || out.Replaced {
		t.Errorf("same failure: %+v", out)
	}
	if out := verify(t, objs, "api-1", `{"failureType":"OOMKilled"}`); out.Verdict != verdictNew || out.FailureType != "CrashLoopBackOff" {
		t.Errorf("different failure: %+v", out)
	}
	healthy := testfixtures.HealthyPod("shop", "web-1")
	if out := verify(t, []runtime.Object{healthy}, "web-1", `{"failureType":"OOMKilled"}`); out.Verdict != verdictFixed || out.Pod.Ready != "1/1" {
		t.Errorf("healthy: %+v", out)
	}
}

// The usual case: the fix rolled the Deployment, the diagnosed pod is
// gone, and the check follows the workload to its newest pod.
func TestVerify_FollowsTheWorkloadToTheNewPod(t *testing.T) {
	old := testfixtures.HealthyPod("shop", "web-5d8f7-old11")
	old.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	old.Status.ContainerStatuses[0].Ready = false
	newer := testfixtures.HealthyPod("shop", "web-6c9a2-new22")
	newer.CreationTimestamp = metav1.NewTime(time.Now())
	other := testfixtures.HealthyPod("shop", "webhook-1")
	other.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Hour)) // newest, but another workload

	out := verify(t, []runtime.Object{old, newer, other}, "web-gone-pod", `{"failureType":"OOMKilled","owner":"deployment/web"}`)
	if out.Verdict != verdictFixed || !out.Replaced || out.Pod.Name != "web-6c9a2-new22" {
		t.Errorf("follow workload: %+v", out)
	}
	if !strings.Contains(out.Message, `new pod "web-6c9a2-new22"`) {
		t.Errorf("message = %q", out.Message)
	}

	if out := verify(t, []runtime.Object{newer}, "gone", `{"failureType":"OOMKilled","owner":"pod/gone"}`); out.Verdict != verdictNotFound {
		t.Errorf("bare pod gone: %+v", out)
	}
}

// A pod that has only just come up must not be called fixed: crashes
// and OOMs typically arrive seconds later.
func TestVerify_JustStartedIsNotYetFixed(t *testing.T) {
	p := testfixtures.HealthyPod("shop", "web-1")
	p.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(time.Now().Add(-3 * time.Second))
	out := verify(t, []runtime.Object{p}, "web-1", `{"failureType":"OOMKilled"}`)
	if out.Verdict != verdictStarting || !strings.Contains(out.Message, "checking again to confirm it stays up") {
		t.Errorf("just started: %+v", out)
	}
}

func TestVerify_StartingPod(t *testing.T) {
	p := testfixtures.HealthyPod("shop", "web-1")
	p.Status.Phase = corev1.PodPending
	p.Status.ContainerStatuses[0].Ready = false
	p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}
	if out := verify(t, []runtime.Object{p}, "web-1", `{"failureType":"OOMKilled"}`); out.Verdict != verdictStarting {
		t.Errorf("starting: %+v", out)
	}
}
