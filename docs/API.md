# KubeAssist backend-go — API Reference

Generated against the actual routes registered in `internal/api/handlers.go`
(`NewRouter`). If a route is added/removed there, this doc should be
updated in the same commit — it's meant to describe reality, not intent.

All responses are `application/json` unless noted otherwise (SSE stream
excepted). All error responses use the envelope:

```json
{ "code": "short_snake_case_code", "message": "human-readable detail" }
```

---

## Cluster inspection (read-only)

### `GET /api/health`
Liveness check. Always `200 {"status":"ok"}` if the process is up —
does not verify Kubernetes connectivity.

### `GET /api/cluster`
```json
{ "name": "kind-kubeassist-dev", "serverVersion": "v1.31.0" }
```
`name` is whatever `KUBE_CONTEXT` was configured with; `serverVersion` is
fetched once at startup via `clientset.Discovery().ServerVersion()`.

### `GET /api/namespaces`
```json
["default", "kube-system", "kubeassist"]
```

### `GET /api/pods?namespace=`
`namespace` query param is optional — omit for all namespaces.
```json
[
  {
    "name": "crashloop-test-7fbdf49cdd-4d48b",
    "namespace": "default",
    "phase": "Running",
    "ready": "0/1",
    "restartCount": 21,
    "waitingReason": "CrashLoopBackOff"
  }
]
```
Lightweight summaries only — no logs, no events, no detection. For full
evidence use `GET /api/pods/{ns}/{name}`.

### `GET /api/pods/{ns}/{name}`
Returns the full `DiagnosticSnapshot` (see Schemas below) with **no
detection run** — `detector.failureType` will always be empty here.
This is the raw-evidence endpoint; detection only runs as part of
`/troubleshoot`.

### `GET /api/pods/{ns}/{name}/logs?container=&previous=`
```json
{ "logs": "starting up...\nfatal: simulated crash\n" }
```
`container` defaults to the pod's first container if omitted.
`previous=true` fetches the prior container instance's logs (only
meaningful if `restartCount > 0`).

---

## Troubleshooting (detection + AI diagnosis)

### `POST /api/pods/{ns}/{name}/troubleshoot`
Request body (all fields optional):
```json
{ "question": "why does this keep restarting?", "serviceName": "broken-service" }
```
- `question`: free-text, attached to the snapshot as `userQuestion`, sent
  to the AI service for context — never used by Go's own detection.
- `serviceName`: if given, pass-2 collection evaluates exactly that
  Service (rather than scanning every Service in the namespace) for a
  selector/endpoint mismatch.

**Pipeline:** `CollectCore` → `Detect` → conditionally `CollectRelated`
(pass-2, triggered by `Healthy`/`Unknown`/`CreateContainerConfigError`
classification or an explicit `serviceName`) → `Redact` → **one** POST
to the Python AI service → (if configured) persist to Postgres → respond.

There is no multi-pass exchange with Python — evidence gathering is
entirely a Go-side decision made *before* the single AI call.

Response: a `DiagnosisResponse` (see Schemas below). If no AI service is
configured (`AI_BASE_URL`/`AI_AUTH_TOKEN` unset), returns a degraded
`mode: "detector_only_stub"` response built entirely from Go's own
detector evidence — still a valid, schema-compliant diagnosis, just
without RAG-backed remediation steps or citations.

### `GET /api/pods/{ns}/{name}/troubleshoot/stream`
Server-Sent Events. Emits pipeline stage events as they happen:
```
data: {"stage":"collecting"}

data: {"stage":"detecting"}

data: {"stage":"retrieving"}

data: {"stage":"diagnosing"}

event: done
data: { ...full DiagnosisResponse... }

```
Consume with the browser's native `EventSource` API. Chosen over
WebSockets because this is a one-directional, server-push-only stream —
see `internal/api/handlers.go`'s doc comment on `handleTroubleshootStream`
for the full tradeoff discussion.

---

## Investigation history (requires `DATABASE_URL`)

Both routes return `503 {"code":"store_unavailable"}` if Postgres isn't
configured.

### `GET /api/investigations/{id}`
Returns one saved `Investigation` (snapshot + diagnosis + metadata).

### `GET /api/investigations?namespace=&limit=`
Most recent investigations, optionally filtered by namespace.
`limit` defaults to 20.

---

## Remote-cluster relay (requires `AGENT_TOKENS`)

Only registered if at least one cluster token is configured. See
`internal/tunnel` for the underlying agent/relay architecture.

### `GET /agent/connect`
WebSocket upgrade endpoint — consumed by `cmd/agent`, not by a frontend
or API client. Requires `?clusterId=&token=` query params matching a
configured `AGENT_TOKENS` entry.

### `POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot`
**Requires `Authorization: Bearer <token>`** — the token returned once
from `POST /api/clusters` at registration time. Returns `401
{"code":"invalid_cluster_token"}` if missing or incorrect — this is what
prevents one customer from querying a different customer's cluster.

Same request/response shape as the local troubleshoot endpoint, but the
snapshot is fetched via the connected agent for `clusterId` instead of a
local `client-go` clientset. Returns `502 {"code":"agent_unreachable"}`
if no agent is currently connected for that cluster ID.

**Known gap:** this path does not yet run pass-2 `CollectRelated` — the
tunnel protocol would need a second round trip to support that.

### `GET /api/remote/{clusterId}/status`
**Requires `Authorization: Bearer <token>`**, same as troubleshoot above.
```json
{ "connected": true }
```

---

## Schemas

The wire contract with the Python AI service is formally defined in
`schemas/diagnostic-snapshot.schema.json` (Go → Python) and
`schemas/diagnosis.schema.json` (Python → Go), both currently at
`schemaVersion: "1.1.0"`. `internal/model` is a direct, field-for-field
Go mirror of both — if you're unsure what a field means, the schema
files have the authoritative descriptions.

Both schemas use `"additionalProperties": false` — every field sent must
be declared, every required field must be present. Notably: required
**array** fields must be JSON arrays, never `null` — a nil Go slice
marshals to `null` and will fail validation, so every code path
constructing these types must initialize array fields explicitly (see
`stubDiagnosis` in `internal/api/handlers.go` for the reference pattern).
