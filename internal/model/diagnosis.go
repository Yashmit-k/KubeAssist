package model

// DiagnosisResponse is what the Python AI service returns from
// POST /ai/troubleshoot, matching diagnosis.schema.json exactly.
//
// ResolutionSteps changed from []string to []ResolutionStep — each step
// is now a structured object with an explicit order, an optional
// suggested command, and an IsWriteAction flag marking commands that
// MUTATE cluster state (delete, scale, patch) as distinct from
// read-only ones (get, describe, logs). This matters because your
// original brief's "never automatically trust AI-generated commands"
// principle needs a machine-readable signal for which commands are even
// candidates for future approval-gated execution — a plain string
// can't carry that.
//
// NOTE: there is NO recollect/multi-pass protocol on the wire.
// needsRecollect/recollectHints were discussed and explicitly left out
// of schema 1.1.0 (decision D4, confirmed directly with the schema's
// author) — this is a single request/response exchange, full stop. Any
// "gather more evidence" logic happens ENTIRELY inside Go, before the
// one and only POST to Python — see internal/api's two-pass collection
// (CollectCore -> Detect -> conditionally CollectRelated) for where that
// actually lives.
type DiagnosisResponse struct {
	SchemaVersion           string             `json:"schemaVersion"`
	FailureType             string             `json:"failureType"`
	IssueSummary            string             `json:"issueSummary"`
	ObservedEvidence        []string           `json:"observedEvidence"`
	ProbableRootCause       string             `json:"probableRootCause"`
	Reasoning               string             `json:"reasoning"`
	ConfidenceScore         float64            `json:"confidenceScore"`
	ResolutionSteps         []ResolutionStep   `json:"resolutionSteps"`
	VerificationCommands    []string           `json:"verificationCommands"`
	RollbackGuidance        string             `json:"rollbackGuidance"`
	DocumentationReferences []DocumentationRef `json:"documentationReferences"`
	UncertaintyNotes        []string           `json:"uncertaintyNotes"`
	// Mode self-identifies which path produced this response ("full_rag"
	// | "detector_only_stub" | "mock_llm") — this field IS in the schema
	// (optional, not required), unlike the recollect fields above.
	Mode string `json:"mode,omitempty"`
}

// ResolutionStep is one ordered remediation step. Command is a pointer
// (nullable) since the schema explicitly allows `["string", "null"]` —
// not every step has an associated kubectl command (e.g. "check your
// application's database connection string" has no command at all).
type ResolutionStep struct {
	Order         int     `json:"order"`
	Description   string  `json:"description"`
	Command       *string `json:"command,omitempty"`
	IsWriteAction bool    `json:"isWriteAction,omitempty"`
}

// DocumentationRef is one citation the RAG pipeline retrieved.
type DocumentationRef struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Section string `json:"section,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// TroubleshootRequest is the body accepted at
// POST /api/pods/{ns}/{name}/troubleshoot. ServiceName is optional and
// supplied by the CALLER (React, or a direct API user) up front — it is
// NOT something Python requests mid-conversation (there is no
// multi-turn handshake). If omitted, Go's own pass-2 collection falls
// back to scanning every Service in the namespace and matching
// selectors/endpoints against the pod's labels itself.
type TroubleshootRequest struct {
	Question    string  `json:"question,omitempty"`
	ServiceName *string `json:"serviceName,omitempty"`
}

// Investigation is what gets persisted to Postgres and returned by
// GET /api/investigations/{id}.
type Investigation struct {
	ID        string             `json:"id"`
	Namespace string             `json:"namespace"`
	PodName   string             `json:"podName"`
	Snapshot  DiagnosticSnapshot `json:"snapshot"`
	Diagnosis DiagnosisResponse  `json:"diagnosis"`
	CreatedAt string             `json:"createdAt"`
}
