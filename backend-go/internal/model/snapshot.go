// Package model defines the shared data contracts (DTOs) used across the
// Go backend. As of schemaVersion "1.1.0", these structs are a DIRECT,
// FIELD-FOR-FIELD MIRROR of two formally published JSON Schema files —
// diagnostic-snapshot.schema.json (this file) and diagnosis.schema.json
// (diagnosis.go) — which are the actual, enforced wire contract with the
// Python AI service. Both schemas use "additionalProperties": false,
// meaning EVERY field sent must be declared in the schema, and every
// REQUIRED field must be present — extra fields and missing fields both
// fail validation. This file's job is to make it structurally
// IMPOSSIBLE to accidentally drift from that contract: if a Go struct
// here doesn't match the schema, json.Marshal will produce invalid
// output, not silently succeed.
package model

import "time"

// schemaVersion is a package-level constant (not exported, since only
// this package needs to stamp it onto outgoing snapshots) matching the
// schema's `"schemaVersion": {"const": "1.1.0"}`. Centralizing it here
// means bumping the contract version later is a one-line change, not a
// find-and-replace across every place a snapshot gets constructed.
const schemaVersion = "1.1.0"

// SchemaVersion exposes the constant for callers (collector, tests)
// that need to stamp it onto a snapshot without duplicating the literal
// string.
func SchemaVersion() string { return schemaVersion }

// DiagnosticSnapshot is the full evidence bundle Go gathers about a
// single pod before asking the Python AI service to diagnose it.
type DiagnosticSnapshot struct {
	SchemaVersion  string          `json:"schemaVersion"`
	Cluster        ClusterInfo     `json:"cluster"`
	Pod            PodInfo         `json:"pod"`
	InitContainers []ContainerInfo `json:"initContainers,omitempty"`
	Containers     []ContainerInfo `json:"containers"`
	Conditions     []Condition     `json:"conditions"`
	Events         []Event         `json:"events"`
	Logs           LogBundle       `json:"logs"`
	Owners         *OwnerInfo      `json:"owners,omitempty"`
	Related        *RelatedInfo    `json:"related,omitempty"`
	Detector       DetectorResult  `json:"detector"`
	UserQuestion   string          `json:"userQuestion,omitempty"`
	// ServiceName lets the user (or a re-collection round) tell Go which
	// specific Service to evaluate for a selector/endpoint mismatch,
	// rather than Go guessing across every Service in the namespace —
	// new in schemaVersion 1.1.0.
	ServiceName *string   `json:"serviceName,omitempty"`
	CollectedAt time.Time `json:"collectedAt"`
}

// ClusterInfo identifies which cluster this evidence came from. Both
// fields are REQUIRED by the schema — a snapshot with an empty
// ClusterInfo is schema-valid (empty strings are still strings) but
// diagnostically useless, which is why internal/api now populates this
// from Server.ClusterName/ServerVersion before every send — previously
// this was a real, silent gap: nothing ever set it.
type ClusterInfo struct {
	Name          string `json:"name"`
	ServerVersion string `json:"serverVersion"`
}

// PodInfo is the pod-level metadata.
type PodInfo struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Phase       string            `json:"phase"`
	Reason      string            `json:"reason,omitempty"`
	Message     string            `json:"message,omitempty"`
	NodeName    string            `json:"nodeName,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	// OwnerRefs changed from []string to structured objects to match
	// the schema — previously we threw away the apiVersion/kind
	// distinction and just stored names as bare strings.
	OwnerRefs []OwnerRef `json:"ownerRefs,omitempty"`
}

// OwnerRef mirrors a single Kubernetes OwnerReference entry.
type OwnerRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// ContainerInfo captures per-container health signals.
type ContainerInfo struct {
	Name         string          `json:"name"`
	Image        string          `json:"image"`
	Ready        bool            `json:"ready"`
	RestartCount int32           `json:"restartCount"`
	State        ContainerState  `json:"state"`
	LastState    *ContainerState `json:"lastState,omitempty"`
	Resources    ResourceSummary `json:"resources"`
	Probes       ProbeSummary    `json:"probes"`
}

// ContainerState mirrors Kubernetes' own waiting/running/terminated
// union, flattened with a Phase discriminator string (JSON has no
// native tagged-union concept).
type ContainerState struct {
	Phase      string `json:"phase"` // "waiting" | "running" | "terminated"
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	ExitCode   *int32 `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// ResourceSummary now matches the schema's shape: a distinguished
// LimitsMemory field (used directly by detectOOMKilled's evidence text)
// plus generic Requests/Limits maps keyed by Kubernetes resource name
// ("cpu", "memory", "ephemeral-storage", ...) rather than our old
// hardcoded RequestsCPU/RequestsMemory/LimitsCPU/LimitsMemory fields.
// This is more flexible — e.g. ephemeral-storage limits (relevant to
// Evicted diagnosis) now have somewhere to live without another schema
// change.
type ResourceSummary struct {
	LimitsMemory string            `json:"limitsMemory,omitempty"`
	Requests     map[string]string `json:"requests,omitempty"`
	Limits       map[string]string `json:"limits,omitempty"`
}

// ProbeSummary is a redacted, human-readable summary of the three probe
// types Kubernetes supports.
type ProbeSummary struct {
	Liveness  string `json:"liveness,omitempty"`
	Readiness string `json:"readiness,omitempty"`
	Startup   string `json:"startup,omitempty"`
}

// Condition mirrors Kubernetes PodCondition.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// Event mirrors a Kubernetes Event object.
type Event struct {
	Type          string    `json:"type"`
	Reason        string    `json:"reason"`
	Message       string    `json:"message"`
	Count         int32     `json:"count,omitempty"`
	LastTimestamp time.Time `json:"lastTimestamp,omitempty"`
}

// LogBundle now includes Truncated and UnavailableReason, both REQUIRED
// by the schema (unlike Current/Previous, they cannot be omitted even
// when zero-valued — hence no `omitempty` on those two tags). Previous
// changed to a plain pointer (no omitempty) so it serializes as an
// explicit `null` when there's no previous instance to have logs from,
// rather than being omitted — the schema requires the KEY to always be
// present, with null as one of its valid values.
type LogBundle struct {
	Current           string  `json:"current"`
	Previous          *string `json:"previous"`
	Truncated         bool    `json:"truncated"`
	UnavailableReason string  `json:"unavailableReason"`
}

// OwnerInfo now nests ReplicaSet/Deployment as structured refs rather
// than flat name/strategy fields directly on OwnerInfo, and adds
// YamlExcerpts — matching the schema's `owners: {replicaSet, deployment,
// yamlExcerpts}` shape.
type OwnerInfo struct {
	ReplicaSet   *ReplicaSetRef `json:"replicaSet,omitempty"`
	Deployment   *DeploymentRef `json:"deployment,omitempty"`
	YamlExcerpts []string       `json:"yamlExcerpts,omitempty"`
}

type ReplicaSetRef struct {
	Name            string `json:"name"`
	DesiredReplicas int32  `json:"desiredReplicas,omitempty"`
}

type DeploymentRef struct {
	Name     string `json:"name"`
	Strategy string `json:"strategy,omitempty"`
}

// RelatedInfo is populated only on "pass 2" of the collector. The
// schema's `related` object only declares `services` and
// `configMapRefs` — no secrets field at all, consistent with the RBAC
// policy of never fetching Secret values (or even names, as of this
// schema revision — a stricter stance than our earlier draft, which had
// a SecretNames field we're now removing since it has nowhere valid to
// go in the wire contract).
type RelatedInfo struct {
	Services      []ServiceSummary `json:"services,omitempty"`
	ConfigMapRefs []ConfigMapRef   `json:"configMapRefs,omitempty"`
}

// ConfigMapRef replaces the old flat ConfigMapNames []string — the
// schema wants to know not just WHICH ConfigMaps are referenced, but
// which of their expected keys are present vs. missing, since a missing
// key (not a missing ConfigMap) is the actual CreateContainerConfigError
// signature in many real cases.
type ConfigMapRef struct {
	Name        string   `json:"name"`
	KeysPresent []string `json:"keysPresent,omitempty"`
	KeysMissing []string `json:"keysMissing,omitempty"`
}

// ServiceSummary is intentionally thin — selector + endpoint count is
// enough to detect a mismatch.
type ServiceSummary struct {
	Name           string            `json:"name"`
	Selector       map[string]string `json:"selector"`
	EndpointsReady int               `json:"endpointsReady"`
}

// DetectorResult is Go's own deterministic classification.
type DetectorResult struct {
	FailureType  string   `json:"failureType"`
	Confidence   float64  `json:"confidence"`
	Evidence     []string `json:"evidence"`
	SearchTerms  []string `json:"searchTerms"`
	RelatedHints []string `json:"relatedHints,omitempty"`

	// Fix holds concrete remediation computed from the evidence (see
	// detect.PlanFix), so suggested commands carry real values instead
	// of template placeholders.
	Fix *FixPlan `json:"fix,omitempty"`
}

// FixPlan is the evidence-derived fix for one diagnosis. Facts are the
// exact values found in the snapshot (memory limit, failing image,
// missing ConfigMap key, ...); Steps are ready-to-run commands built
// from them. A "<...>" placeholder remains only where the evidence
// cannot know the answer (e.g. which image tag was intended), and the
// step's description says so.
type FixPlan struct {
	Facts map[string]string `json:"facts,omitempty"`
	Steps []FixStep         `json:"steps,omitempty"`
}

// FixStep mirrors a DiagnosisResponse resolution step.
type FixStep struct {
	Description   string `json:"description"`
	Command       string `json:"command,omitempty"`
	IsWriteAction bool   `json:"isWriteAction"`
}
