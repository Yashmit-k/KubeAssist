package model

import "time"

// PodSummary is a lighter-weight view of a pod than the full
// DiagnosticSnapshot, used for LIST views (GET /api/pods). We don't want
// to run full log collection for every pod just to render a table.
//
// It lives in model (not collector) because the remote-cluster agent
// also returns pod lists over the tunnel, and internal/tunnel must not
// depend on internal/collector.
type PodSummary struct {
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`
	Phase         string    `json:"phase"`
	Status        string    `json:"status"` // kubectl-style STATUS column, e.g. "CrashLoopBackOff", "Init:Error"
	Ready         string    `json:"ready"`  // e.g. "2/3"
	RestartCount  int32     `json:"restartCount"`
	WaitingReason string    `json:"waitingReason,omitempty"`
	NodeName      string    `json:"nodeName,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// TroubleshootResult is what the Go API returns to its own clients
// (React, curl). It is the AI service's DiagnosisResponse, flattened,
// plus Go-side metadata. The embedded pointer keeps the JSON shape a
// strict superset of diagnosis.schema.json so existing clients keep
// working; the extra fields never travel to or from Python.
type TroubleshootResult struct {
	*DiagnosisResponse
	InvestigationID string         `json:"investigationId,omitempty"`
	Detector        DetectorResult `json:"detector"`
	// AIError is set when the AI service was configured but failed, and
	// the response fell back to the detector-only stub.
	AIError string `json:"aiError,omitempty"`
}
