# KubeAssist Go API

All responses are JSON. Errors use one envelope: `{"code": "...", "message": "..."}`.

Every pod route exists in two forms with identical behaviour:

| Local cluster (local mode only) | Remote cluster (relay; `Authorization: Bearer <cluster token>`) |
| --- | --- |
| `/api/namespaces` | `/api/remote/{clusterId}/namespaces` |
| `/api/pods?namespace=` | `/api/remote/{clusterId}/pods?namespace=` |
| `/api/pods/{ns}/{name}` | `/api/remote/{clusterId}/pods/{ns}/{name}` |
| `/api/pods/{ns}/{name}/logs?container=&previous=` | `/api/remote/{clusterId}/pods/{ns}/{name}/logs?...` |
| `POST /api/pods/{ns}/{name}/troubleshoot` | `POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot` |
| `GET\|POST /api/pods/{ns}/{name}/troubleshoot/stream` | `POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot/stream` |
| `/api/investigations?namespace=&limit=` | `/api/remote/{clusterId}/investigations?...` |
| `/api/investigations/{id}` | `/api/remote/{clusterId}/investigations/{id}` |

With `ENABLE_LOCAL_MODE=false` the left column is not registered at all (404).
Remote routes return `401 invalid_cluster_token` without the right token and
`503 agent_not_connected` when the cluster's agent is offline.

## Health

`GET /api/health` — always 200 while the process is up.

```json
{"status":"ok","version":"v1.0.0","localMode":true,"relay":true,"ai":"ok","store":"ok","features":["local","remote","history"]}
```

`ai` / `store` are `ok`, `unavailable` or `disabled`.

`GET /api/cluster` (local mode) — `{"name":"kind-kubeassist-dev","serverVersion":"v1.36.1"}`

## Pods

`GET .../pods` returns summaries, unhealthy first:

```json
[{"name":"crashloop-app-65f9dc8f4f-b9hkz","namespace":"kubeassist-lab","phase":"Running",
  "status":"CrashLoopBackOff","ready":"0/1","restartCount":6,"waitingReason":"CrashLoopBackOff",
  "nodeName":"kubeassist-dev-control-plane","createdAt":"2026-10-15T15:14:02Z"}]
```

`status` mirrors the `kubectl get pods` STATUS column (`Init:Error`, `Evicted`, ...).

`GET .../pods/{ns}/{name}` returns the redacted pass-1 `DiagnosticSnapshot`
([schema](../schemas/diagnostic-snapshot.schema.json)) with `detector` filled in.
No AI call is made.

`GET .../logs` returns `{"logs": "..."}` (redacted). Asking for `previous=true`
on a container that never restarted returns `{"logs":"","unavailableReason":"..."}`.

## Troubleshoot

`POST .../troubleshoot` with an optional body:

```json
{"question": "Why does it restart after the deploy?", "serviceName": "checkout"}
```

Pipeline: collect (pass 1) → detect → pass 2 (Services/EndpointSlices + ConfigMap
keys) when the pod looks Healthy/Unknown, is CreateContainerConfigError, or
`serviceName` is given → redact → one call to the AI service → save.

Response: a [`DiagnosisResponse`](../schemas/diagnosis.schema.json) plus Go metadata:

```json
{
  "schemaVersion": "1.1.0",
  "failureType": "CrashLoopBackOff",
  "issueSummary": "...",
  "observedEvidence": ["Container \"application\" repeatedly failing: exit code 1 (restarts: 4)", "Log: FATAL: database configuration is invalid"],
  "probableRootCause": "...",
  "reasoning": "...",
  "confidenceScore": 0.81,
  "resolutionSteps": [{"order": 1, "description": "...", "command": "kubectl logs ... --previous", "isWriteAction": false}],
  "verificationCommands": ["..."],
  "rollbackGuidance": "...",
  "documentationReferences": [{"title": "Debug Running Pods", "url": "https://kubernetes.io/...", "section": "..."}],
  "uncertaintyNotes": ["..."],
  "mode": "full_rag | mock_llm | detector_only_stub",
  "investigationId": "a3aecc28-...",
  "detector": {"failureType": "CrashLoopBackOff", "confidence": 0.9, "evidence": ["..."], "searchTerms": ["..."]},
  "aiError": "present only when the AI service failed and the detector-only answer was used"
}
```

`failureType` always equals the Go detector's classification, even if the AI
service returns something else. If the AI service is down the request still
succeeds with `mode: "detector_only_stub"` and `aiError` set.

Errors: `404 pod_not_found`, `502 cluster_error`, `504 timeout`.

### Streaming

`.../troubleshoot/stream` runs the same pipeline as Server-Sent Events. GET takes
`?question=&serviceName=`; POST takes the JSON body (and the remote token header).

```text
data: {"stage":"collecting","message":"Collecting pod, events, owners and logs for ns/pod"}
data: {"stage":"detecting","message":"Detector classified this as CrashLoopBackOff"}
data: {"stage":"related","message":"Checking related Services and ConfigMap keys"}   (pass 2 only)
data: {"stage":"diagnosing","message":"Retrieving Kubernetes docs and generating a diagnosis"}
data: {"stage":"saving","message":"Saving investigation"}                            (with a DB)
event: done
data: { ...same body as POST troubleshoot... }
```

Failures arrive as `event: error` with `{"code","message"}`. `: keepalive`
comments are sent every 15 s.

## Investigation history

Requires `DATABASE_URL` (else `503 store_unavailable`). History is scoped per
cluster: local history only through the local routes, a remote cluster's only
with its token.

`GET .../investigations` → `[{"id","clusterId","namespace","podName","failureType","issueSummary","confidenceScore","mode","createdAt"}]`

`GET .../investigations/{id}` → `{"id","clusterId","namespace","podName","snapshot":{...},"diagnosis":{...},"createdAt"}`

## Remote clusters (ENABLE_RELAY=true)

`POST /api/clusters` `{"clusterId":"prod-eu-1"}` → `201`

```json
{"clusterId":"prod-eu-1","token":"<48 hex chars, shown once>","relayUrl":"wss://.../agent/connect","manifest":"<complete agent YAML>"}
```

IDs are DNS labels (`[a-z0-9-]`, ≤63). `409` if taken. Only a SHA-256 hash of
the token is stored (Postgres when configured, so registrations survive restarts).

`GET /api/remote/{clusterId}/status` → `{"clusterId":"...","connected":true,"connectedAt":"..."}`

`DELETE /api/clusters/{clusterId}` (cluster token) → `204`; disconnects the agent,
invalidates the token, deletes that cluster's history.

`GET /api/clusters` lists every registration. Operator-only: requires
`Authorization: Bearer $ADMIN_TOKEN` when set; otherwise only available in local mode.

`GET /agent/connect` — the agent's WebSocket. Credentials in headers:
`Authorization: Bearer <token>` and `X-Cluster-ID: <id>` (query params
`?clusterId=&token=` are still accepted from older agents).

### Tunnel protocol

JSON messages over the WebSocket. Relay → agent:

```json
{"jobId":"9f2c...","kind":"snapshot|related|namespaces|pods|logs","namespace":"...","pod":"...","serviceName":"...","container":"...","previous":false}
```

Agent → relay: `{"jobId","snapshot"|"related"|"namespaces"|"pods"|"logs","error","notFound"}`.
An empty `kind` means `snapshot` (agents that predate job kinds). The relay pings
every 30 s; the agent redacts everything before sending it.
