# KubeAssist Go API

All responses are JSON. Errors use one envelope: `{"code": "...", "message": "..."}`.

Every pod route exists in two forms with identical behaviour:

| Local cluster (local mode only) | Remote cluster (relay; session cookie of the cluster's owner) |
| --- | --- |
| `/api/namespaces` | `/api/remote/{clusterId}/namespaces` |
| `/api/pods?namespace=` | `/api/remote/{clusterId}/pods?namespace=` |
| `/api/pods/{ns}/{name}` | `/api/remote/{clusterId}/pods/{ns}/{name}` |
| `/api/pods/{ns}/{name}/logs?container=&previous=` | `/api/remote/{clusterId}/pods/{ns}/{name}/logs?...` |
| `POST /api/pods/{ns}/{name}/troubleshoot` | `POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot` |
| `GET\|POST /api/pods/{ns}/{name}/troubleshoot/stream` | `POST /api/remote/{clusterId}/pods/{ns}/{name}/troubleshoot/stream` |
| `/api/investigations?namespace=&limit=` | `/api/remote/{clusterId}/investigations?...` |
| `/api/investigations/{id}` | `/api/remote/{clusterId}/investigations/{id}` |
| `POST /api/scan?namespace=` | `POST /api/remote/{clusterId}/scan?namespace=` |
| `POST /api/pods/{ns}/{name}/verify` | `POST /api/remote/{clusterId}/pods/{ns}/{name}/verify` |
| `POST /api/chat` | `POST /api/remote/{clusterId}/chat` |

With `ENABLE_LOCAL_MODE=false` the left column is not registered at all (404).
Local routes need no sign-in (local mode is single-operator).

Remote routes are authorized on every request by the browser session (see
[Accounts](#accounts)): `401 unauthenticated` when signed out,
`404 cluster_not_found` when the cluster doesn't exist **or belongs to someone
else** (the two are indistinguishable), and `503 agent_not_connected` when the
cluster's agent is offline. An operator may instead send
`Authorization: Bearer $ADMIN_TOKEN`, which reaches every cluster. Agent tokens
authenticate agents only; they are not accepted on these routes.

Troubleshoot routes (local and remote) are rate limited per user (per client IP
when anonymous): `429 rate_limited` with a `Retry-After` header.

## Health

`GET /api/health` — always 200 while the process is up.

```json
{"status":"ok","version":"v1.0.0","localMode":true,"relay":true,"ai":"ok","store":"ok","features":["local","remote","history"]}
```

`ai` / `store` are `ok`, `unavailable` or `disabled`. `features` may also include
`accounts`; `authProviders` lists the enabled sign-in methods (`github`, `dev`),
empty when the relay (and so accounts) is off.

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
`serviceName` is given → plan the fix → redact → one call to the AI service → save.

"Plan the fix" (`detect.PlanFix`) turns the evidence into `detector.fix` on the
snapshot: `facts` (the exact values involved, e.g. `memoryLimit: 32Mi`,
`configMap: application-config`, `missingKey: database-url`) and `steps`
(ready-to-run commands built from them). The playbook answers and the LLM prompt
use these, so fix commands carry real values; a `<placeholder>` remains only for
what the evidence cannot know (an intended image tag, a config value), and the
UI highlights it. Nothing is ever executed by KubeAssist.

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

## What changed (rollout timeline)

For a pod owned by a Deployment, the snapshot includes `rollout`: the revision the
pod runs, when it last went live (`rolledOutAt`), whether it is the newest
revision, and the pod-template diff from the previous revision (images,
command/args, env, resources, probes, volumes, service account). Env values of
sensitive-looking names (`*PASS*`, `*TOKEN*`, `*SECRET*`, `*KEY*`, …) are never
included — only that they changed.

When the failing pod runs the newest revision, rolled out within 7 days with
template changes, the diagnosis gains an evidence line ("Runs revision 4 of
deployment/x, rolled out 2m ago; changed from revision 3: image … → …") and a
final fix step `kubectl rollout undo deployment/x -n ns --to-revision=3`.
`make lab-rollout` deploys a lab Deployment whose second revision breaks it.

The UI exports any diagnosis (with its latest verify result) or scan as a
Markdown incident report; that is client-side only.

## Health scan

`POST .../scan?namespace=` (namespace optional) checks every pod that isn't cleanly
Running and Ready (or that has restarts; at most 40), classifies each with the
detector, plans its fix, and groups pods by **root cause** — the broken thing, not
the pod — so pods sharing a cause (replicas of one Deployment, or two Deployments
reading the same missing ConfigMap key) appear once with one fix. No AI call.

```json
{"namespace":"kubeassist-lab","scannedAt":"...","totalPods":11,"checked":11,"healthy":2,"truncated":false,
 "groups":[{"key":"configmap-key:kubeassist-lab/application-config/database-url",
            "title":"ConfigMap \"application-config\" is missing key \"database-url\"",
            "failureType":"CreateContainerConfigError",
            "pods":[{"namespace":"kubeassist-lab","name":"missing-config-app-7b9745dbc4-gn9w6","status":"CreateContainerConfigError","ready":"0/1","restartCount":0}],
            "evidence":["..."], "fix":{"facts":{...},"steps":[...]}}],
 "errors":[]}
```

Groups are ordered by number of pods. Rate limited like troubleshoot.

## Verify a fix

`POST .../pods/{ns}/{name}/verify` with `{"failureType": "<before>", "owner": "deployment/api", "serviceName": "..."}`
(all optional) re-collects the pod — or, when the fix rolled out a new pod, the
newest pod of `owner` — and compares with the diagnosis the user acted on:

```json
{"verdict":"still_failing","message":"The new pod \"oom-failure-b6d96d897-rn444\" still shows OOMKilled. ...",
 "pod":{"name":"oom-failure-b6d96d897-rn444","status":"CrashLoopBackOff","ready":"0/1","restartCount":5},
 "replaced":true,"failureType":"OOMKilled","evidence":["..."],"fix":{...},"checkedAt":"..."}
```

`verdict` is `fixed`, `starting` (still coming up, or Running for under 30 s — a
crash or OOM usually follows within seconds; check again), `still_failing`,
`new_problem` (with the next fix) or `not_found`. Read-only.

## Chat

`POST .../chat` with `{"messages":[{"role":"user","content":"Why is oom-failure crashing?"}]}`
(up to 20 text turns, last one from the user) →

```json
{"reply":"**kubeassist-lab/oom-failure-...**: out of memory (`OOMKilled`) ...","mode":"rules",
 "tools":[{"name":"diagnose_pod","arguments":{"name":"oom-failure"},"summary":"Diagnosed kubeassist-lab/oom-failure-...: OOMKilled","ok":true}]}
```

The AI service chooses read-only tools (`scan`, `diagnose_pod`, `get_logs`,
`list_pods`, `list_namespaces`); the backend runs them against the cluster and
feeds results back, for at most 4 rounds. With an LLM configured (`mode: "llm"`)
the model plans and writes the answer; without one (`mode: "rules"`) a keyword
planner picks one tool and the answer is composed from its result. Pod names may
be partial; ambiguous names come back as candidates. Needs the AI service (`503
ai_unavailable` otherwise). Nothing is ever executed on the cluster.

## Alerts (remote clusters, owner only)

KubeAssist scans every cluster that has alerts enabled every `ALERT_INTERVAL`
(default 60 s) using the health scan, and posts to the owner's webhook when a
root cause **starts** or is **resolved** — one message per check, batching
everything that changed. A change must be seen in 2 consecutive scans before it
is reported (pods that flap between crash and "Running" don't spam), problems
that already exist when alerts are enabled are tracked silently (no alert storm),
and clusters whose agent is offline are skipped, never "resolved".

`GET /api/remote/{clusterId}/alerts` →

```json
{"configured":true,"kind":"slack","url":"https://hooks.slack.com/…1234","enabled":true,"updatedAt":"...","interval":"1m0s",
 "status":{"lastScanAt":"...","baseline":true,"active":[{"key":"...","title":"...","failureType":"OOMKilled","pods":["lab/oom-1"],"fix":"kubectl ...","since":"..."}],
           "lastNotifiedAt":"...","lastDeliveryError":"..."}}
```

The webhook URL is a secret and is only ever returned masked.

`PUT .../alerts` `{"kind":"slack|discord|webhook","url":"https://...","enabled":true}` saves the
config (omit `url` to keep the stored one). `DELETE .../alerts` removes it (revoking
the cluster removes it too). `POST .../alerts/test` sends a test message → `{"ok":true}`
or `{"ok":false,"error":"..."}`.

Payloads: Slack `{"text"}`, Discord `{"content"}` (≤2000 chars), generic
`{"source":"kubeassist","event":"change|test","cluster","new":[cause],"resolved":[cause],"url"}`.

Webhook URLs are fetched by the server, so they must be `https`, may not carry
credentials, and the connection is refused when the host resolves to a private,
loopback, link-local (incl. cloud metadata), CGNAT or multicast address; redirects
are not followed. `ALERTS_ALLOW_PRIVATE_WEBHOOKS=true` lifts this for local
development only.

## Investigation history

Requires `DATABASE_URL` (else `503 store_unavailable`). History is scoped per
cluster: local history only through the local routes, a remote cluster's only
for its owner.

`GET .../investigations` → `[{"id","clusterId","namespace","podName","failureType","issueSummary","confidenceScore","mode","createdAt"}]`

`GET .../investigations/{id}` → `{"id","clusterId","namespace","podName","snapshot":{...},"diagnosis":{...},"createdAt"}`

## Accounts

Enabled whenever the relay is (`ENABLE_RELAY=true`). Users sign in through an
identity provider; KubeAssist stores no passwords. The session is an opaque
random token in an `HttpOnly`, `SameSite=Lax` cookie (`kubeassist_session`, or
`__Host-kubeassist_session` + `Secure` when `PUBLIC_URL` is https); only its
SHA-256 hash is stored. Sessions last `SESSION_TTL` (30 days).

State-changing requests that carry the cookie must come from a trusted
`Origin` (the request's own host, `PUBLIC_URL`, or `CORS_ALLOWED_ORIGINS`),
else `403 untrusted_origin`.

`GET /api/auth/github/login?next=/connect` → `302` to GitHub (state + PKCE).
GitHub redirects back to `GET /api/auth/github/callback`, which creates/updates
the user (keyed by GitHub's numeric user ID), sets the session cookie and
redirects to `PUBLIC_URL` + `next` (relative paths only). On failure it redirects
to `PUBLIC_URL/connect?authError=<code>`.

`POST /api/auth/dev-login` `{"login":"alice"}` → `200` user + cookie. Only when
`AUTH_DEV_LOGIN=true`: signs anyone in as any username. Development/CI only;
refused at startup when `PUBLIC_URL` is https.

`GET /api/auth/me` → `{"id":"<uuid>","provider":"github","login":"octocat","name":"...","avatarUrl":"..."}`, or `401 unauthenticated`.

`POST /api/auth/logout` → `204`; deletes the session and clears the cookie.

Sign-in endpoints are rate limited per client IP (`RATE_LIMIT_LOGIN`).

## Remote clusters (ENABLE_RELAY=true)

`POST /api/clusters` `{"clusterId":"prod-eu-1"}` (signed in) → `201`

```json
{"clusterId":"prod-eu-1","token":"<agent token, 48 hex chars, shown once>","relayUrl":"wss://.../agent/connect","manifest":"<complete agent YAML>"}
```

The cluster is owned by the caller. IDs are DNS labels (`[a-z0-9-]`, ≤63) and
globally unique: `409` if taken. `401` when signed out,
`403 cluster_limit_reached` beyond `MAX_CLUSTERS_PER_USER`, `429 rate_limited`
beyond `RATE_LIMIT_REGISTER` per user. The token is for the agent only (it is
embedded in the manifest); only its SHA-256 hash is stored (Postgres when
configured, so registrations survive restarts). With `ADMIN_TOKEN` instead of
a session, the registration has no owner and only the operator can reach it.

`GET /api/clusters` → the caller's clusters,
`[{"clusterId":"...","connected":true,"connectedAt":"..."}]`. With
`ADMIN_TOKEN`: every registration.

`GET /api/remote/{clusterId}/status` → `{"clusterId":"...","connected":true,"connectedAt":"..."}`

`DELETE /api/clusters/{clusterId}` (owner or admin) → `204`; disconnects the agent,
invalidates its token, deletes that cluster's history.

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
