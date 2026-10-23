package fixpr

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// ErrNoMatch means the file has no document for the target.
var ErrNoMatch = errors.New("no matching manifest in this file")

// docSeparator is a YAML document start line ("---", optionally with a
// comment). Files are edited one document at a time so the others stay
// byte-for-byte identical.
var docSeparator = regexp.MustCompile(`(?m)^---[ \t]*(#.*)?$`)

type chunk struct{ start, end int }

func splitDocs(src []byte) []chunk {
	var out []chunk
	prev := 0
	for _, m := range docSeparator.FindAllIndex(src, -1) {
		out = append(out, chunk{prev, m[0]})
		prev = m[1]
	}
	return append(out, chunk{prev, len(src)})
}

// editDoc is one parsed document being changed. Changes to existing
// scalars are recorded as positions so they can be spliced into the
// original text; anything structural marks the document dirty and it is
// re-encoded instead.
type editDoc struct {
	root    *yaml.Node // the document's top mapping
	dirty   bool
	splices []splice
	changes []string // human-readable, for the PR
}

type splice struct {
	line, col int // 1-based, of the scalar in the chunk
	old       *yaml.Node
	text      string // replacement YAML text for the scalar
}

// Matches reports whether a document is the target object.
func matches(doc *yaml.Node, t Target) bool {
	if doc == nil || doc.Kind != yaml.MappingNode {
		return false
	}
	if scalar(get(doc, "kind")) != t.Kind {
		return false
	}
	meta := get(doc, "metadata")
	if scalar(get(meta, "name")) != t.Name {
		return false
	}
	ns := scalar(get(meta, "namespace"))
	return ns == "" || ns == t.Namespace
}

// ApplyToFile finds the target document in src and applies edit to it.
// It returns the new file content and a description of each change.
func ApplyToFile(src []byte, t Target, edit func(*editDoc) error) ([]byte, []string, error) {
	chunks := splitDocs(src)
	found := -1
	var doc *editDoc
	for i, c := range chunks {
		var n yaml.Node
		if err := yaml.Unmarshal(src[c.start:c.end], &n); err != nil || len(n.Content) == 0 {
			continue // not YAML we can read, or empty: never the target
		}
		if matches(n.Content[0], t) {
			if found >= 0 {
				return nil, nil, fmt.Errorf("the file has two %s %q documents", t.Kind, t.Name)
			}
			found, doc = i, &editDoc{root: n.Content[0]}
		}
	}
	if found < 0 {
		return nil, nil, ErrNoMatch
	}
	if err := edit(doc); err != nil {
		return nil, nil, err
	}
	if len(doc.changes) == 0 {
		return nil, nil, errors.New("the manifest already has these values; nothing to change")
	}
	c := chunks[found]
	original := src[c.start:c.end]
	var replaced []byte
	if doc.dirty {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(doc.root); err != nil {
			return nil, nil, err
		}
		_ = enc.Close()
		lead := original[:len(original)-len(bytes.TrimLeft(original, "\r\n"))]
		trail := original[len(bytes.TrimRight(original, " \t\r\n")):]
		replaced = append(append(append([]byte{}, lead...), bytes.TrimRight(buf.Bytes(), "\n")...), trail...)
	} else {
		var err error
		if replaced, err = applySplices(original, doc.splices); err != nil {
			return nil, nil, err
		}
	}
	out := append(append(append([]byte{}, src[:c.start]...), replaced...), src[c.end:]...)
	// The result must still parse, or the PR would break someone's deploy.
	var check yaml.Node
	if err := yaml.Unmarshal(replaced, &check); err != nil {
		return nil, nil, fmt.Errorf("edited manifest no longer parses: %w", err)
	}
	return out, doc.changes, nil
}

// applySplices replaces scalars in place, last first so offsets hold.
func applySplices(src []byte, sp []splice) ([]byte, error) {
	lines := bytes.SplitAfter(src, []byte("\n"))
	sort.Slice(sp, func(i, j int) bool {
		if sp[i].line != sp[j].line {
			return sp[i].line > sp[j].line
		}
		return sp[i].col > sp[j].col
	})
	for _, s := range sp {
		if s.line < 1 || s.line > len(lines) {
			return nil, errors.New("scalar position out of range")
		}
		ln := lines[s.line-1]
		start := s.col - 1
		end, err := scalarEnd(ln, start, s.old.Style)
		if err != nil {
			return nil, err
		}
		lines[s.line-1] = append(append(append([]byte{}, ln[:start]...), s.text...), ln[end:]...)
	}
	return bytes.Join(lines, nil), nil
}

// scalarEnd finds where a single-line scalar starting at i ends.
func scalarEnd(line []byte, i int, style yaml.Style) (int, error) {
	if i < 0 || i >= len(line) {
		return 0, errors.New("scalar column out of range")
	}
	switch {
	case style&yaml.DoubleQuotedStyle != 0:
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\\' {
				j++
				continue
			}
			if line[j] == '"' {
				return j + 1, nil
			}
		}
	case style&yaml.SingleQuotedStyle != 0:
		for j := i + 1; j < len(line); j++ {
			if line[j] == '\'' {
				if j+1 < len(line) && line[j+1] == '\'' {
					j++
					continue
				}
				return j + 1, nil
			}
		}
	case style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return 0, errors.New("multi-line scalars are re-encoded, not spliced")
	default:
		end := len(bytes.TrimRight(line, "\r\n"))
		if k := bytes.Index(line[i:end], []byte(" #")); k >= 0 {
			end = i + k
		}
		if k := bytes.IndexAny(line[i:end], ",]}"); k >= 0 && inFlow(line[:i]) {
			end = i + k
		}
		return i + len(bytes.TrimRight(line[i:end], " \t")), nil
	}
	return 0, errors.New("unterminated quoted scalar")
}

func inFlow(prefix []byte) bool {
	return bytes.Count(prefix, []byte("[")) > bytes.Count(prefix, []byte("]")) || bytes.Count(prefix, []byte("{")) > bytes.Count(prefix, []byte("}"))
}

// --- node helpers ---

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// ensureMap returns m[key] as a mapping, creating it (and marking the
// document dirty) when missing.
func (d *editDoc) ensureMap(m *yaml.Node, key string) (*yaml.Node, error) {
	if v := get(m, key); v != nil {
		if v.Kind != yaml.MappingNode {
			if v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
				v.Kind, v.Tag, v.Value, v.Style = yaml.MappingNode, "!!map", "", 0
				d.dirty = true
				return v, nil
			}
			return nil, fmt.Errorf("%q is not a mapping", key)
		}
		return v, nil
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	d.dirty = true
	return v, nil
}

// set sets m[key] to a scalar: spliced if it exists, added otherwise.
// isInt keeps numbers unquoted.
func (d *editDoc) set(m *yaml.Node, key, value string, isInt bool, change string) error {
	tag := "!!str"
	if isInt {
		tag = "!!int"
	}
	if v := get(m, key); v != nil {
		if v.Kind != yaml.ScalarNode {
			return fmt.Errorf("%q is not a plain value", key)
		}
		if v.Value == value {
			return nil
		}
		text, err := scalarText(value, isInt, v.Style)
		if err != nil {
			return err
		}
		if v.Line > 0 && v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
			d.splices = append(d.splices, splice{line: v.Line, col: v.Column, old: v, text: text})
		} else {
			d.dirty = true
		}
		v.Value, v.Tag = value, tag
		if !isInt && v.Style == 0 && strings.HasPrefix(text, "\"") {
			v.Style = yaml.DoubleQuotedStyle
		}
		d.changes = append(d.changes, change)
		return nil
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	d.dirty = true
	d.changes = append(d.changes, change)
	return nil
}

// scalarText renders a value as YAML in the original's quoting style.
func scalarText(value string, isInt bool, style yaml.Style) (string, error) {
	switch {
	case isInt:
		if _, err := strconv.Atoi(value); err != nil {
			return "", fmt.Errorf("%q is not a number", value)
		}
		return value, nil
	case style&yaml.DoubleQuotedStyle != 0:
		return strconv.Quote(value), nil
	case style&yaml.SingleQuotedStyle != 0:
		return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
	}
	// Plain: let the encoder decide whether it needs quotes.
	b, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// container finds the named container (or init container) in a
// workload's pod template.
func container(doc *yaml.Node, name string) (*yaml.Node, error) {
	spec := get(get(get(doc, "spec"), "template"), "spec")
	for _, list := range []string{"containers", "initContainers"} {
		seq := get(spec, list)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, c := range seq.Content {
			if scalar(get(c, "name")) == name {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("container %q is not in the manifest's pod template", name)
}

// setEnv sets a literal env var: spliced when it exists with a value,
// added otherwise. Vars read from ConfigMaps/Secrets aren't touched.
func (d *editDoc) setEnv(c *yaml.Node, name, value string) error {
	env := get(c, "env")
	if env != nil && env.Kind == yaml.SequenceNode {
		for _, e := range env.Content {
			if scalar(get(e, "name")) != name {
				continue
			}
			if get(e, "valueFrom") != nil {
				return fmt.Errorf("env %s is read from a ConfigMap/Secret in the manifest; change it there", name)
			}
			return d.set(e, "value", value, false, fmt.Sprintf("env %s = %q", name, value))
		}
	}
	if env == nil {
		env = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		c.Content = append(c.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "env"}, env)
	}
	env.Content = append(env.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "value"}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	}})
	d.dirty = true
	d.changes = append(d.changes, fmt.Sprintf("env %s = %q (added)", name, value))
	return nil
}

// removeEnv deletes an env var.
func (d *editDoc) removeEnv(c *yaml.Node, name string) error {
	env := get(c, "env")
	if env == nil || env.Kind != yaml.SequenceNode {
		return nil
	}
	for i, e := range env.Content {
		if scalar(get(e, "name")) == name {
			env.Content = append(env.Content[:i], env.Content[i+1:]...)
			d.dirty = true
			d.changes = append(d.changes, fmt.Sprintf("env %s removed", name))
			return nil
		}
	}
	return nil
}
