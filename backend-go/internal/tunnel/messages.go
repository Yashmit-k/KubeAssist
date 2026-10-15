// Package tunnel implements a reverse-connection relay: instead of the
// backend reaching INTO a customer's cluster (which would require their
// cluster to accept inbound internet traffic — usually a non-starter),
// a small agent running INSIDE the customer's cluster connects OUTBOUND
// to this relay and holds the connection open. The relay then sends
// work down that already-open connection and gets results back.
//
// Outbound connections from inside a cluster are almost always allowed
// by default (that's how it pulls images); inbound ones need the
// customer to expose something. By making the AGENT dial out, we never
// ask the customer to open anything, and their kube credentials never
// leave their cluster.
package tunnel

import "github.com/Yashmit-k/KubeAssist/backend-go/internal/model"

// JobKind selects what the agent should do for a JobRequest.
type JobKind string

const (
	// JobSnapshot runs pass-1 collection (CollectCore). It is also the
	// meaning of an EMPTY Kind, so agents built before job kinds existed
	// keep working for troubleshooting.
	JobSnapshot JobKind = "snapshot"
	// JobRelated runs pass-2 collection (Services/EndpointSlices and
	// ConfigMap keys) — the second tunnel round-trip that lets the
	// remote path detect ServiceSelectorMismatch and missing keys.
	JobRelated        JobKind = "related"
	JobListNamespaces JobKind = "namespaces"
	JobListPods       JobKind = "pods"
	JobLogs           JobKind = "logs"
)

// JobRequest is what the relay sends DOWN the tunnel to a connected
// agent. JobID correlates responses with requests, since several jobs
// can be in flight on one connection and responses may arrive out of
// order (the same correlation-ID pattern as JSON-RPC).
type JobRequest struct {
	JobID       string  `json:"jobId"`
	Kind        JobKind `json:"kind,omitempty"`
	Namespace   string  `json:"namespace,omitempty"`
	Pod         string  `json:"pod,omitempty"`
	ServiceName *string `json:"serviceName,omitempty"`
	Container   string  `json:"container,omitempty"`
	Previous    bool    `json:"previous,omitempty"`
}

// JobResponse is what the agent sends BACK up the tunnel. Error is
// checked first; otherwise the field matching the request's Kind is set.
// Everything in here has already been redacted by the agent.
type JobResponse struct {
	JobID      string                    `json:"jobId"`
	Snapshot   *model.DiagnosticSnapshot `json:"snapshot,omitempty"`
	Related    *model.RelatedInfo        `json:"related,omitempty"`
	Namespaces []string                  `json:"namespaces,omitempty"`
	Pods       []model.PodSummary        `json:"pods,omitempty"`
	Logs       *string                   `json:"logs,omitempty"`
	Error      string                    `json:"error,omitempty"`
	// NotFound distinguishes "that pod doesn't exist" from agent
	// failures, so the API can answer 404 instead of 502.
	NotFound bool `json:"notFound,omitempty"`
}
