# backend-go

The KubeAssist hub: the only component that talks to Kubernetes, the AI service, Postgres and agents. See the [root README](../README.md) for the big picture and [docs/API.md](../docs/API.md) for every route.

| Package | Responsibility |
| --- | --- |
| `cmd/server` | Wiring only: config → clients → `api.Server` → HTTP with graceful shutdown |
| `cmd/agent` | In-cluster agent: dials the relay, runs collection jobs, redacts, replies |
| `internal/collector` | Pass-1 snapshot (pod, events, owners, failing container's logs) and pass-2 (Services/EndpointSlices, ConfigMap keys). Uses `kubernetes.Interface`, tested with the fake clientset |
| `internal/detect` | Deterministic, priority-ordered failure classification. Pure functions over a snapshot |
| `internal/redact` | Secret scrubbing for every free-text field |
| `internal/api` | HTTP handlers; one troubleshoot pipeline shared by POST/SSE and local/remote (`clusterSource`); detector-only playbook |
| `internal/ai` | Client for `POST /ai/troubleshoot` with timeout, one retry on 429/5xx, backoff + jitter |
| `internal/store` | Postgres: cluster-scoped investigations, cluster registrations (token hashes) |
| `internal/tunnel` | Relay hub: agent WebSockets, job routing by ID, keepalive, token verification |
| `internal/model` | Go mirror of `/schemas` (1.1.0) |

```bash
go test -race ./...                       # unit + schema contract + tunnel tests
TEST_DATABASE_URL=postgres://... go test ./internal/store/   # Postgres integration
go run ./cmd/server                       # local mode against $KUBECONFIG
```

The contract tests in `internal/api/contract_test.go` validate real collector output and every detector-only diagnosis against `../schemas`, so Go/Python drift fails here rather than as a `400` from the AI service.
