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

// extraPatterns cover common credential formats that don't follow the
// key=value shape above.
var extraPatterns = []*regexp.Regexp{
	// JWTs (three base64url segments, header starting "eyJ").
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
	// GitHub, Slack, Stripe/OpenAI-style and Google API keys.
	regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\b(sk|rk|pk)[-_](live|test|proj|ant)?[-_]?[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
}

// urlCredentials matches user:password@ in connection URLs
// (postgres://app:hunter2@db:5432/x) and keeps the scheme and user.
var urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+):[^\s@/]+@`)

// Snapshot redacts every free-text field of a snapshot IN PLACE and
// returns it for convenience. It must run before a snapshot leaves Go
// (to the AI service, to Postgres, or up the agent tunnel).
func Snapshot(s *model.DiagnosticSnapshot) *model.DiagnosticSnapshot {
	s.Logs.Current = Text(s.Logs.Current)
	if s.Logs.Previous != nil {
		redacted := Text(*s.Logs.Previous)
		s.Logs.Previous = &redacted
	}
	s.Logs.UnavailableReason = Text(s.Logs.UnavailableReason)
	s.Pod.Message = Text(s.Pod.Message)
	for i := range s.Events {
		s.Events[i].Message = Text(s.Events[i].Message)
	}
	for i := range s.Conditions {
		s.Conditions[i].Message = Text(s.Conditions[i].Message)
	}
	redactContainers(s.Containers)
	redactContainers(s.InitContainers)
	s.UserQuestion = Text(s.UserQuestion)

	// last-applied-configuration embeds the whole manifest, including
	// any literal env values — drop it rather than try to scrub it.
	delete(s.Pod.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	for k, v := range s.Pod.Annotations {
		s.Pod.Annotations[k] = Text(v)
	}
	return s
}

func redactContainers(cs []model.ContainerInfo) {
	for i := range cs {
		cs[i].State.Message = Text(cs[i].State.Message)
		if cs[i].LastState != nil {
			cs[i].LastState.Message = Text(cs[i].LastState.Message)
		}
	}
}

// Text redacts one free-text string.
func Text(text string) string {
	if text == "" {
		return text
	}
	text = urlCredentials.ReplaceAllString(text, "${1}:"+maskedValue+"@")
	for _, p := range patterns {
		text = p.ReplaceAllString(text, maskedValue)
	}
	for _, p := range extraPatterns {
		text = p.ReplaceAllString(text, maskedValue)
	}
	return text
}
