// Package fixpr turns a diagnosis into a pull request against the
// user's manifests: it derives the fixes that can be expressed as an
// exact edit to a workload or ConfigMap manifest, and applies one to the
// YAML file that defines it.
package fixpr

import (
	"fmt"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// Target is the object whose manifest is edited.
type Target struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// Input is a value the user supplies (one the evidence can't know).
type Input struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Default     string `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

// Option is one fix that can be opened as a pull request.
type Option struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Target      Target  `json:"target"`
	Inputs      []Input `json:"inputs"`
	// Unavailable explains why this fix can't be a PR (it is still shown).
	Unavailable string `json:"unavailable,omitempty"`

	edit func(d *editDoc, in map[string]string) error
}

// Apply edits a manifest file for this option.
func (o Option) Apply(src []byte, inputs map[string]string) ([]byte, []string, error) {
	if o.Unavailable != "" {
		return nil, nil, fmt.Errorf("%s", o.Unavailable)
	}
	for _, in := range o.Inputs {
		if strings.TrimSpace(inputs[in.Name]) == "" && in.Default == "" {
			return nil, nil, fmt.Errorf("%s is required", in.Label)
		}
	}
	vals := map[string]string{}
	for _, in := range o.Inputs {
		v := strings.TrimSpace(inputs[in.Name])
		if v == "" {
			v = in.Default
		}
		vals[in.Name] = v
	}
	return ApplyToFile(src, o.Target, func(d *editDoc) error { return o.edit(d, vals) })
}

var workloadKinds = map[string]string{"deployment": "Deployment", "statefulset": "StatefulSet", "daemonset": "DaemonSet"}

// Options lists the PR-able fixes for an enriched snapshot (detector
// result with its fix plan and rollout). Pods without a workload get
// none: a bare pod's manifest is whatever created it.
func Options(s *model.DiagnosticSnapshot) []Option {
	f := map[string]string{}
	if s.Detector.Fix != nil && s.Detector.Fix.Facts != nil {
		f = s.Detector.Fix.Facts
	}
	ns := s.Pod.Namespace
	kind, name, _ := strings.Cut(f["owner"], "/")
	wl := Target{Kind: workloadKinds[kind], Name: name, Namespace: ns}
	ctr := f["container"]
	var out []Option
	if wl.Kind != "" && ctr != "" {
		switch s.Detector.FailureType {
		case "OOMKilled":
			if def := f["suggestedMemoryLimit"]; def != "" {
				out = append(out, memoryOption(wl, ctr, f["memoryLimit"], def))
			}
		case "ImagePullBackOff":
			out = append(out, imageOption(wl, ctr, f["image"]))
		case "FailedScheduling":
			if f["requestsCpu"] != "" || f["requestsMemory"] != "" {
				out = append(out, requestsOption(wl, ctr, f["requestsCpu"], f["requestsMemory"]))
			}
		case "ProbeFailure", "ReadinessFailure":
			if o, ok := probeOption(wl, ctr, f); ok {
				out = append(out, o)
			}
		}
		if s.Rollout != nil && f["rolloutRevision"] != "" {
			out = append(out, revertOption(wl, s.Rollout))
		}
	}
	if f["configMap"] != "" && f["missingKey"] != "" {
		out = append(out, configKeyOption(Target{Kind: "ConfigMap", Name: f["configMap"], Namespace: ns}, f["missingKey"]))
	}
	return out
}

func validQuantity(label, v string) error {
	if _, err := resource.ParseQuantity(v); err != nil {
		return fmt.Errorf("%s %q is not a Kubernetes quantity (e.g. 256Mi, 500m)", label, v)
	}
	return nil
}

func memoryOption(t Target, ctr, current, suggested string) Option {
	return Option{
		ID: "raise-memory", Target: t,
		Title:       fmt.Sprintf("Raise %s's memory limit to %s", t.Name, suggested),
		Description: fmt.Sprintf("Sets resources.limits.memory of container %q from %s to the value below.", ctr, current),
		Inputs:      []Input{{Name: "memory", Label: "Memory limit", Default: suggested}},
		edit: func(d *editDoc, in map[string]string) error {
			if err := validQuantity("memory limit", in["memory"]); err != nil {
				return err
			}
			c, err := container(d.root, ctr)
			if err != nil {
				return err
			}
			res, err := d.ensureMap(c, "resources")
			if err != nil {
				return err
			}
			lim, err := d.ensureMap(res, "limits")
			if err != nil {
				return err
			}
			return d.set(lim, "memory", in["memory"], false, fmt.Sprintf("container %s: resources.limits.memory = %s", ctr, in["memory"]))
		},
	}
}

func imageOption(t Target, ctr, current string) Option {
	return Option{
		ID: "set-image", Target: t,
		Title:       fmt.Sprintf("Fix %s's image", t.Name),
		Description: fmt.Sprintf("Container %q can't pull %q. Enter the image it should run.", ctr, current),
		Inputs:      []Input{{Name: "image", Label: "Image", Placeholder: "registry/name:tag"}},
		edit: func(d *editDoc, in map[string]string) error {
			img := in["image"]
			if strings.ContainsAny(img, " \t\n") || img == current {
				return fmt.Errorf("enter a different, valid image reference")
			}
			c, err := container(d.root, ctr)
			if err != nil {
				return err
			}
			return d.set(c, "image", img, false, fmt.Sprintf("container %s: image = %s", ctr, img))
		},
	}
}

func requestsOption(t Target, ctr, cpu, mem string) Option {
	return Option{
		ID: "set-requests", Target: t,
		Title:       fmt.Sprintf("Lower %s's resource requests", t.Name),
		Description: fmt.Sprintf("No node fits the current requests (cpu=%s, memory=%s). Enter what the app needs and a node can offer; limits are raised to match if they're lower.", or(cpu, "unset"), or(mem, "unset")),
		Inputs:      []Input{{Name: "cpu", Label: "CPU request", Placeholder: "250m"}, {Name: "memory", Label: "Memory request", Placeholder: "256Mi"}},
		edit: func(d *editDoc, in map[string]string) error {
			for _, k := range []string{"cpu", "memory"} {
				if err := validQuantity(k+" request", in[k]); err != nil {
					return err
				}
			}
			c, err := container(d.root, ctr)
			if err != nil {
				return err
			}
			res, err := d.ensureMap(c, "resources")
			if err != nil {
				return err
			}
			req, err := d.ensureMap(res, "requests")
			if err != nil {
				return err
			}
			for _, k := range []string{"cpu", "memory"} {
				if err := d.set(req, k, in[k], false, fmt.Sprintf("container %s: resources.requests.%s = %s", ctr, k, in[k])); err != nil {
					return err
				}
				// Limits below requests would be rejected by the API server.
				if lim := get(res, "limits"); lim != nil {
					if cur := scalar(get(lim, k)); cur != "" {
						if q, err := resource.ParseQuantity(cur); err == nil {
							if want := resource.MustParse(in[k]); q.Cmp(want) < 0 {
								if err := d.set(lim, k, in[k], false, fmt.Sprintf("container %s: resources.limits.%s = %s (kept ≥ request)", ctr, k, in[k])); err != nil {
									return err
								}
							}
						}
					}
				}
			}
			return nil
		},
	}
}

func probeOption(t Target, ctr string, f map[string]string) (Option, bool) {
	field := "livenessProbe"
	if f["probe"] == "readiness" {
		field = "readinessProbe"
	}
	errText := strings.ToLower(f["probeError"])
	base := Option{ID: "fix-probe", Target: t}
	probe := func(d *editDoc) (*yaml.Node, error) {
		c, err := container(d.root, ctr)
		if err != nil {
			return nil, err
		}
		p := get(c, field)
		if p == nil {
			return nil, fmt.Errorf("container %q has no %s in the manifest", ctr, field)
		}
		return p, nil
	}
	switch {
	case strings.Contains(errText, "connection refused"):
		base.Title = fmt.Sprintf("Point %s's %s at the right port", t.Name, field)
		base.Description = "Nothing listens on the probed port. Enter the port the app listens on."
		base.Inputs = []Input{{Name: "port", Label: "Port", Placeholder: "8080"}}
		base.edit = func(d *editDoc, in map[string]string) error {
			n, err := strconv.Atoi(in["port"])
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("port must be a number from 1 to 65535")
			}
			p, err := probe(d)
			if err != nil {
				return err
			}
			for _, k := range []string{"httpGet", "tcpSocket", "grpc"} {
				if h := get(p, k); h != nil {
					return d.set(h, "port", in["port"], true, fmt.Sprintf("container %s: %s.%s.port = %s", ctr, field, k, in["port"]))
				}
			}
			return fmt.Errorf("the %s has no port to change", field)
		}
	case strings.Contains(errText, "statuscode: 404"):
		base.Title = fmt.Sprintf("Point %s's %s at a path that exists", t.Name, field)
		base.Description = "The probed path returns 404. Enter a path the app serves (e.g. / or /healthz)."
		base.Inputs = []Input{{Name: "path", Label: "Path", Placeholder: "/healthz"}}
		base.edit = func(d *editDoc, in map[string]string) error {
			if !strings.HasPrefix(in["path"], "/") || strings.ContainsAny(in["path"], " \t\n") {
				return fmt.Errorf("path must start with / and contain no spaces")
			}
			p, err := probe(d)
			if err != nil {
				return err
			}
			h := get(p, "httpGet")
			if h == nil {
				return fmt.Errorf("the %s is not an HTTP probe", field)
			}
			return d.set(h, "path", in["path"], false, fmt.Sprintf("container %s: %s.httpGet.path = %s", ctr, field, in["path"]))
		}
	case strings.Contains(errText, "timeout") || strings.Contains(errText, "deadline exceeded"):
		base.Title = fmt.Sprintf("Give %s's %s more time", t.Name, field)
		base.Description = "The app answers too slowly. Raises the initial delay and the timeout."
		base.Inputs = []Input{{Name: "initialDelaySeconds", Label: "Initial delay (s)", Default: "30"}, {Name: "timeoutSeconds", Label: "Timeout (s)", Default: "5"}}
		base.edit = func(d *editDoc, in map[string]string) error {
			p, err := probe(d)
			if err != nil {
				return err
			}
			for _, k := range []string{"initialDelaySeconds", "timeoutSeconds"} {
				if n, err := strconv.Atoi(in[k]); err != nil || n < 0 || n > 3600 {
					return fmt.Errorf("%s must be 0-3600", k)
				}
				if err := d.set(p, k, in[k], true, fmt.Sprintf("container %s: %s.%s = %s", ctr, field, k, in[k])); err != nil {
					return err
				}
			}
			return nil
		}
	default:
		return Option{}, false
	}
	return base, true
}

// revertOption undoes the last rollout's template changes in the
// manifest — the git equivalent of `kubectl rollout undo`, which would
// otherwise be overwritten by the next deploy from git.
func revertOption(t Target, r *model.RolloutInfo) Option {
	o := Option{
		ID: "revert-rollout", Target: t,
		Title:       fmt.Sprintf("Revert %s to revision %d", t.Name, r.PreviousRevision),
		Description: fmt.Sprintf("Undoes what revision %d changed, in the manifest (so the next deploy doesn't bring it back).", r.Revision),
		Inputs:      []Input{},
	}
	var reasons []string
	for _, c := range r.Changes {
		switch c.Field {
		case "image", "resources":
		case "env":
			if strings.Contains(c.From+c.To, "value hidden") || strings.HasPrefix(c.From, "from ") || strings.HasPrefix(c.To, "from ") {
				reasons = append(reasons, fmt.Sprintf("env %s (secret or read from a ConfigMap/Secret)", c.Name))
			}
		default:
			reasons = append(reasons, c.Field)
		}
	}
	if len(reasons) > 0 {
		o.Unavailable = fmt.Sprintf("Revision %d also changed %s, which can't be reverted automatically. Revert it in the manifest by hand, or roll back in the cluster with the rollback command.", r.Revision, strings.Join(reasons, ", "))
		return o
	}
	changes := append([]model.TemplateChange{}, r.Changes...)
	o.edit = func(d *editDoc, _ map[string]string) error {
		for _, ch := range changes {
			c, err := container(d.root, ch.Container)
			if err != nil {
				return err
			}
			switch ch.Field {
			case "image":
				if err := d.set(c, "image", ch.From, false, fmt.Sprintf("container %s: image = %s (was %s)", ch.Container, ch.From, ch.To)); err != nil {
					return err
				}
			case "env":
				switch {
				case ch.From == "" || ch.From == "(not set)":
					if err := d.removeEnv(c, ch.Name); err != nil {
						return err
					}
				default:
					if err := d.setEnv(c, ch.Name, ch.From); err != nil {
						return err
					}
				}
			case "resources":
				if err := setResources(d, c, ch.Container, ch.From); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return o
}

// setResources restores "requests cpu=1,memory=2 limits memory=3".
func setResources(d *editDoc, c *yaml.Node, ctr, text string) error {
	section := ""
	res, err := d.ensureMap(c, "resources")
	if err != nil {
		return err
	}
	for _, tok := range strings.Fields(text) {
		if tok == "requests" || tok == "limits" {
			section = tok
			continue
		}
		if section == "" {
			return fmt.Errorf("can't read resources %q", text)
		}
		m, err := d.ensureMap(res, section)
		if err != nil {
			return err
		}
		for _, kv := range strings.Split(tok, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("can't read resources %q", text)
			}
			if err := d.set(m, k, v, false, fmt.Sprintf("container %s: resources.%s.%s = %s", ctr, section, k, v)); err != nil {
				return err
			}
		}
	}
	return nil
}

func configKeyOption(t Target, key string) Option {
	return Option{
		ID: "add-config-key", Target: t,
		Title:       fmt.Sprintf("Add key %q to ConfigMap %s", key, t.Name),
		Description: "The workload reads this key but the ConfigMap doesn't have it. Enter the value the app expects. Don't put secrets in a ConfigMap — use a Secret for those.",
		Inputs:      []Input{{Name: "value", Label: key}},
		edit: func(d *editDoc, in map[string]string) error {
			data, err := d.ensureMap(d.root, "data")
			if err != nil {
				return err
			}
			if get(data, key) != nil {
				return fmt.Errorf("the manifest already defines %q; apply it to the cluster instead", key)
			}
			return d.set(data, key, in["value"], false, fmt.Sprintf("data.%s set", key))
		},
	}
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
