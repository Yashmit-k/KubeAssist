package redact

import (
	"strings"
	"testing"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

func TestSnapshot_RedactsLogSecrets(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		mustNotHave string // substring that must NOT survive redaction
	}{
		{
			name:        "bearer token",
			input:       "calling API with Authorization: Bearer sk-abc123XYZ.token-here",
			mustNotHave: "sk-abc123XYZ",
		},
		{
			name:        "password key=value",
			input:       "connecting to db with password=SuperSecret123!",
			mustNotHave: "SuperSecret123",
		},
		{
			name:        "api key colon form",
			input:       `config loaded: api_key: "sk-live-1234567890"`,
			mustNotHave: "sk-live-1234567890",
		},
		{
			name:        "PEM private key block",
			input:       "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----",
			mustNotHave: "MIIEpAIBAAKCAQEA",
		},
		{
			name:        "AWS access key",
			input:       "using credentials AKIAABCDEFGHIJKLMNOP for upload",
			mustNotHave: "AKIAABCDEFGHIJKLMNOP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &model.DiagnosticSnapshot{Logs: model.LogBundle{Current: tt.input}}
			result := Snapshot(snapshot)

			if strings.Contains(result.Logs.Current, tt.mustNotHave) {
				t.Errorf("redacted log still contains secret %q; got: %q", tt.mustNotHave, result.Logs.Current)
			}
			if !strings.Contains(result.Logs.Current, maskedValue) {
				t.Errorf("redacted log missing mask marker %q; got: %q", maskedValue, result.Logs.Current)
			}
		})
	}
}

func TestSnapshot_PreservesNonSecretText(t *testing.T) {
	input := "server started on port 8080, listening for requests"
	snapshot := &model.DiagnosticSnapshot{Logs: model.LogBundle{Current: input}}
	result := Snapshot(snapshot)

	if result.Logs.Current != input {
		t.Errorf("non-secret log text was modified: got %q, want %q", result.Logs.Current, input)
	}
}

func TestSnapshot_DropsLastAppliedConfigAnnotation(t *testing.T) {
	snapshot := &model.DiagnosticSnapshot{
		Pod: model.PodInfo{
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","kind":"Secret","data":{"password":"c2VjcmV0"}}`,
				"some-other-annotation":                            "keep-me",
			},
		},
	}
	result := Snapshot(snapshot)

	if _, exists := result.Pod.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; exists {
		t.Error("last-applied-configuration annotation should have been dropped entirely")
	}
	if result.Pod.Annotations["some-other-annotation"] != "keep-me" {
		t.Error("unrelated annotation should have been preserved")
	}
}

func TestText_ExtendedPatterns(t *testing.T) {
	cases := map[string]string{
		"jwt":          "auth eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U ok",
		"github token": "cloning with ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"slack token":  "posting via xoxb-1234567890-abcdefghij",
		"url creds":    "dial postgres://app:hunter2secret@db:5432/orders failed",
		"openai key":   "using sk-proj-abcdefghijklmnopqrstuv1234",
	}
	secrets := map[string]string{
		"jwt": "dozjgNryP4J3jVmNHl0w5N", "github token": "ghp_abcdef", "slack token": "xoxb-1234567890",
		"url creds": "hunter2secret", "openai key": "abcdefghijklmnopqrstuv1234",
	}
	for name, in := range cases {
		if out := Text(in); strings.Contains(out, secrets[name]) {
			t.Errorf("%s survived redaction: %q", name, out)
		}
	}
	// The username and host in a URL are useful evidence and should stay.
	if out := Text(cases["url creds"]); !strings.Contains(out, "postgres://app:") || !strings.Contains(out, "@db:5432") {
		t.Errorf("url redaction removed too much: %q", out)
	}
}

func TestSnapshot_RedactsInitContainersConditionsAndQuestion(t *testing.T) {
	s := &model.DiagnosticSnapshot{
		InitContainers: []model.ContainerInfo{{State: model.ContainerState{Message: "token=abc123secret"}}},
		Conditions:     []model.Condition{{Message: "password: letmein"}},
		UserQuestion:   "is api_key=zzz-secret-zzz the problem?",
	}
	Snapshot(s)
	for _, v := range []string{s.InitContainers[0].State.Message, s.Conditions[0].Message, s.UserQuestion} {
		if strings.Contains(v, "abc123secret") || strings.Contains(v, "letmein") || strings.Contains(v, "zzz-secret-zzz") {
			t.Errorf("secret survived: %q", v)
		}
	}
}
