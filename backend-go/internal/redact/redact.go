// Package redact scrubs sensitive data from a DiagnosticSnapshot BEFORE
// it's allowed to leave the Go process — this is the concrete
// implementation of the plan's trust boundary: "Python receives a
// redacted DiagnosticSnapshot JSON. Never kube credentials, never
// Secret/data values."
//
// WHY REGEX-BASED SCRUBBING OF LOG TEXT, INSTEAD OF JUST NOT COLLECTING
// SECRETS IN THE FIRST PLACE?
// We already avoid fetching Secret data entirely — as of schemaVersion
// 1.1.0, model.RelatedInfo doesn't even carry Secret NAMES anymore
// (only ConfigMapRefs), a stricter stance than our original draft. But
// logs are free-form text written by arbitrary application code, and a
// misbehaving app can absolutely log a database password, an API key,
// or a bearer token directly. We can't prevent that upstream, so
// redact's job is defense-in-depth: scan unstructured text (logs, event
// messages) for PATTERNS that look like secrets, and mask them, even
// though we don't know the app's code.
package redact

import (
	"regexp"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

const maskedValue = "***REDACTED***"

// patterns are compiled ONCE at package init time (via var + MustCompile
// at package scope), not on every call to Redact. Compiling a regex is
// relatively expensive (it builds an internal state machine); compiling
// once and reusing the compiled *regexp.Regexp for every snapshot is a
// meaningful performance win if this runs on every troubleshoot request.
var patterns = []*regexp.Regexp{
	// Bearer tokens: "Bearer <token>" in Authorization headers logged
	// by an HTTP client/server.
	regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9\-_.]+`),

	// Generic key=value or key: value secrets — password, secret, token,
	// api_key, apikey, access_key — case-insensitive, common separator
	// variants (=, :, "=", ": ").
	regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key|access[_-]?key)\s*[:=]\s*\S+`),

	// PEM blocks — private keys, certificates. These are multi-line, so
	// we match the BEGIN/END markers and everything between them
	// non-greedily with (?s) enabling '.' to match newlines.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.*?-----END [A-Z ]+-----`),

	// AWS-style access key IDs (AKIA followed by 16 uppercase
	// alphanumerics) — specific enough to be a high-confidence pattern
	// worth its own rule rather than relying on the generic one above.
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
}

// Snapshot redacts sensitive content from a DiagnosticSnapshot IN PLACE
// and returns it for convenient chaining. We mutate in place (rather
// than returning a deep copy) because by the time redact runs, the
// collector's local copy of the snapshot is about to be sent onward —
// there's no other reader relying on the unredacted version afterward,
// so a copy would just be wasted allocation.
func Snapshot(s *model.DiagnosticSnapshot) *model.DiagnosticSnapshot {
	s.Logs.Current = redactText(s.Logs.Current)
	if s.Logs.Previous != nil {
		redacted := redactText(*s.Logs.Previous)
		s.Logs.Previous = &redacted
	}

	for i := range s.Events {
		s.Events[i].Message = redactText(s.Events[i].Message)
	}

	for i := range s.Containers {
		s.Containers[i].State.Message = redactText(s.Containers[i].State.Message)
		if s.Containers[i].LastState != nil {
			s.Containers[i].LastState.Message = redactText(s.Containers[i].LastState.Message)
		}
	}

	// Pod annotations frequently carry things like
	// "kubectl.kubernetes.io/last-applied-configuration" which can
	// embed an entire prior Secret manifest if someone `kubectl apply`'d
	// a Secret with client-side apply. We drop this specific annotation
	// entirely rather than trying to regex-scrub structured YAML/JSON
	// inside a string — too easy to miss a field that way.
	delete(s.Pod.Annotations, "kubectl.kubernetes.io/last-applied-configuration")

	return s
}

func redactText(text string) string {
	for _, p := range patterns {
		text = p.ReplaceAllString(text, maskedValue)
	}
	return text
}
