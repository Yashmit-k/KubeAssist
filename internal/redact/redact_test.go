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
