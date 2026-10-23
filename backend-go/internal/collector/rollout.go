package collector

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

const (
	revisionAnnotation = "deployment.kubernetes.io/revision"
	maxTemplateChanges = 20
	maxChangeValue     = 120
)

// collectRollout lines the pod's ReplicaSet up with its Deployment's
// other ReplicaSets (one per revision) and diffs the pod template
// against the previous revision. Failures here only cost the timeline,
// never the diagnosis, so they return nil.
func (c *Collector) collectRollout(ctx context.Context, dep *appsv1.Deployment, podRS *appsv1.ReplicaSet) *model.RolloutInfo {
	if dep.Spec.Selector == nil {
		return nil
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil
	}
	list, err := c.clientset.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil
	}

	type rev struct {
		n  int64
		rs *appsv1.ReplicaSet
	}
	var revs []rev
	for i := range list.Items {
		rs := &list.Items[i]
		if !ownedBy(rs.OwnerReferences, "Deployment", dep.Name) {
			continue
		}
		if n, err := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64); err == nil {
			revs = append(revs, rev{n, rs})
		}
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].n < revs[j].n })

	current, err := strconv.ParseInt(podRS.Annotations[revisionAnnotation], 10, 64)
	if err != nil {
		return nil
	}
	info := &model.RolloutInfo{
		Workload:    "deployment/" + dep.Name,
		Revision:    current,
		RolledOutAt: c.rolledOutAt(ctx, podRS),
		Latest:      len(revs) > 0 && revs[len(revs)-1].n == current,
		Changes:     []model.TemplateChange{},
	}
	var prev *appsv1.ReplicaSet
	for _, r := range revs {
		if r.n < current {
			info.PreviousRevision, prev = r.n, r.rs
		}
	}
	if prev != nil {
		t := prev.CreationTimestamp.UTC()
		info.PreviousCreatedAt = &t
		info.Changes = diffTemplates(&prev.Spec.Template.Spec, &podRS.Spec.Template.Spec)
	}
	return info
}

// rolledOutAt is when a revision last went live. A ReplicaSet is reused
// when a Deployment is rolled back or re-applied to a template it has
// seen before, so its creation time can be days older than the rollout;
// its oldest current pod was created when it was last scaled up.
func (c *Collector) rolledOutAt(ctx context.Context, rs *appsv1.ReplicaSet) time.Time {
	at := rs.CreationTimestamp.UTC()
	hash := rs.Labels["pod-template-hash"]
	if hash == "" {
		return at
	}
	pods, err := c.clientset.CoreV1().Pods(rs.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "pod-template-hash=" + hash})
	if err != nil {
		return at
	}
	var oldest time.Time
	for _, p := range pods.Items {
		if !ownedBy(p.OwnerReferences, "ReplicaSet", rs.Name) {
			continue
		}
		if t := p.CreationTimestamp.UTC(); oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	if oldest.After(at) {
		return oldest
	}
	return at
}

func ownedBy(refs []metav1.OwnerReference, kind, name string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

// diffTemplates lists what changed between two pod specs, container by
// container, in a stable order.
func diffTemplates(from, to *corev1.PodSpec) []model.TemplateChange {
	var out []model.TemplateChange
	add := func(c model.TemplateChange) {
		if len(out) < maxTemplateChanges {
			c.From, c.To = clip(c.From), clip(c.To)
			out = append(out, c)
		}
	}
	for _, pair := range [][2][]corev1.Container{{from.InitContainers, to.InitContainers}, {from.Containers, to.Containers}} {
		before := map[string]corev1.Container{}
		for _, c := range pair[0] {
			before[c.Name] = c
		}
		seen := map[string]bool{}
		for _, b := range pair[1] {
			seen[b.Name] = true
			a, ok := before[b.Name]
			if !ok {
				add(model.TemplateChange{Field: "container", Container: b.Name, To: "added"})
				continue
			}
			diffContainer(a, b, add)
		}
		for _, a := range pair[0] {
			if !seen[a.Name] {
				add(model.TemplateChange{Field: "container", Container: a.Name, From: "present", To: "removed"})
			}
		}
	}

	if from.ServiceAccountName != to.ServiceAccountName {
		add(model.TemplateChange{Field: "serviceAccount", From: from.ServiceAccountName, To: to.ServiceAccountName})
	}
	fv, tv := volumeSources(from.Volumes), volumeSources(to.Volumes)
	for _, name := range sortedKeys(fv, tv) {
		if fv[name] != tv[name] {
			add(model.TemplateChange{Field: "volume", Name: name, From: fv[name], To: tv[name]})
		}
	}
	return out
}

func diffContainer(a, b corev1.Container, add func(model.TemplateChange)) {
	ch := func(field, from, to string) {
		if from != to {
			add(model.TemplateChange{Field: field, Container: b.Name, From: from, To: to})
		}
	}
	ch("image", a.Image, b.Image)
	ch("command", strings.Join(a.Command, " "), strings.Join(b.Command, " "))
	ch("args", strings.Join(a.Args, " "), strings.Join(b.Args, " "))
	ch("resources", resourcesText(a.Resources), resourcesText(b.Resources))
	pa, pb := toProbeSummary(a), toProbeSummary(b)
	ch("livenessProbe", pa.Liveness, pb.Liveness)
	ch("readinessProbe", pa.Readiness, pb.Readiness)
	ch("startupProbe", pa.Startup, pb.Startup)

	ea, eb := envValues(a.Env), envValues(b.Env)
	for _, name := range sortedKeys(ea, eb) {
		from, inFrom := ea[name]
		to, inTo := eb[name]
		if inFrom && inTo && from == to {
			continue
		}
		if sensitiveName.MatchString(name) {
			// Report that it changed, never what to.
			from, to = presence(inFrom), presence(inTo)
			if inFrom && inTo {
				from, to = "(value hidden)", "(changed, value hidden)"
			}
		} else {
			if !inFrom {
				from = ""
			}
			if !inTo {
				to = "(removed)"
			}
		}
		add(model.TemplateChange{Field: "env", Container: b.Name, Name: name, From: from, To: to})
	}
}

// sensitiveName matches env var names whose values must never leave the
// cluster in a diff.
var sensitiveName = regexp.MustCompile(`(?i)pass|secret|token|key|credential|auth|private|cert|dsn|conn`)

func presence(present bool) string {
	if present {
		return "(set)"
	}
	return "(not set)"
}

// envValues describes each env var: its literal value, or where it is
// read from (ConfigMap/Secret key, field, resource).
func envValues(env []corev1.EnvVar) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		switch vf := e.ValueFrom; {
		case vf == nil:
			out[e.Name] = e.Value
		case vf.ConfigMapKeyRef != nil:
			out[e.Name] = fmt.Sprintf("from ConfigMap %s/%s", vf.ConfigMapKeyRef.Name, vf.ConfigMapKeyRef.Key)
		case vf.SecretKeyRef != nil:
			out[e.Name] = fmt.Sprintf("from Secret %s/%s", vf.SecretKeyRef.Name, vf.SecretKeyRef.Key)
		case vf.FieldRef != nil:
			out[e.Name] = "from field " + vf.FieldRef.FieldPath
		case vf.ResourceFieldRef != nil:
			out[e.Name] = "from resource " + vf.ResourceFieldRef.Resource
		default:
			out[e.Name] = "(computed)"
		}
	}
	return out
}

func resourcesText(r corev1.ResourceRequirements) string {
	s := toResourceSummary(r)
	part := func(label string, m map[string]string) string {
		if len(m) == 0 {
			return ""
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kv := make([]string, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, k+"="+m[k])
		}
		return label + " " + strings.Join(kv, ",")
	}
	return strings.TrimSpace(strings.Join([]string{part("requests", s.Requests), part("limits", s.Limits)}, " "))
}

func volumeSources(vols []corev1.Volume) map[string]string {
	out := make(map[string]string, len(vols))
	for _, v := range vols {
		switch {
		case v.ConfigMap != nil:
			out[v.Name] = "ConfigMap " + v.ConfigMap.Name
		case v.Secret != nil:
			out[v.Name] = "Secret " + v.Secret.SecretName
		case v.PersistentVolumeClaim != nil:
			out[v.Name] = "PVC " + v.PersistentVolumeClaim.ClaimName
		case v.EmptyDir != nil:
			out[v.Name] = "emptyDir"
		default:
			out[v.Name] = "other"
		}
	}
	return out
}

func sortedKeys(a, b map[string]string) []string {
	set := map[string]bool{}
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func clip(s string) string {
	if len(s) > maxChangeValue {
		return s[:maxChangeValue] + "…"
	}
	return s
}
