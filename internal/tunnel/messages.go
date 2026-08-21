// Package tunnel implements a reverse-connection relay: instead of the
// backend reaching INTO a customer's cluster (which would require their
// cluster to accept inbound internet traffic — usually a non-starter),
// a small agent running INSIDE the customer's cluster connects OUTBOUND
// to this relay and holds the connection open. The relay then sends
// work down that already-open connection and gets results back.
//
// WHY THIS AVOIDS THE INBOUND-FIREWALL PROBLEM
// Outbound connections from inside a cluster to the public internet are
// almost always allowed by default — that's how the cluster pulls
// container images, calls external APIs, etc. Inbound connections FROM
// the internet INTO a cluster require the customer to explicitly expose
// something (a LoadBalancer Service, an Ingress, a firewall rule) —
// which most companies are unwilling to do for their Kubernetes API
// server specifically, for good reason. By making the AGENT dial OUT,
// we never ask the customer to open anything.
package tunnel

import "github.com/Yashmit-k/KubeAssist/backend-go/internal/model"

// JobRequest is what the relay sends DOWN the tunnel to a connected
// agent — "go collect evidence for this pod."
//
// WHY A JobID FIELD AT ALL, GIVEN THERE'S ONE JOB PER CONNECTION AT A
// TIME?
// There ISN'T necessarily one job at a time — a single agent connection
// might have several troubleshoot requests in flight concurrently (two
// different users diagnosing two different pods in the same cluster at
// the same moment). JobID lets the relay match each JobResponse back to
// the specific JobRequest that triggered it, since responses can arrive
// out of order over a single shared connection. This is the same
// correlation-ID pattern used in JSON-RPC, HTTP/2 multiplexed streams,
// and most RPC-over-a-single-connection protocols.
type JobRequest struct {
	JobID     string `json:"jobId"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
}

// JobResponse is what the agent sends BACK up the tunnel. Exactly one of
// Snapshot or Error should be set — Go doesn't have tagged unions, so we
// represent this the same way model.ContainerState does: a nil-checkable
// pointer field plus a separate error string, and callers check Error
// first.
type JobResponse struct {
	JobID    string                    `json:"jobId"`
	Snapshot *model.DiagnosticSnapshot `json:"snapshot,omitempty"`
	Error    string                    `json:"error,omitempty"`
}
