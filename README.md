# KubeAssist AI — backend-go

Go read-only Kubernetes collector + detector service for the KubeAssist AI project.

Full route reference: [`docs/API.md`](docs/API.md).
Wire contract with the Python AI service: `schemaVersion` **1.1.0** — see
`internal/model` (a direct field-for-field mirror of the schemas your
friend's AI service validates against).

## Status

Live-cluster verified against a real Kind cluster (`kind-kubeassist-dev`)
across many real failure scenarios — not just unit tests:

| Failure type | Verified live? |
|---|---|
| `CrashLoopBackOff` | Yes — both halves of the restart cycle |
| `OOMKilled` | Yes — correctly wins priority over CrashLoopBackOff |
| `ImagePullBackOff` | Yes |
| `InitContainerFailure` | Yes — both halves of the restart cycle |
| `VolumeMountFailure` | Yes |
| `CreateContainerConfigError`, `FailedScheduling`, `ProbeFailure`, `ReadinessFailure`, `Evicted`, `ServiceSelectorMismatch` | Fixture-tested only, not yet live-verified |
| `Healthy` / `Unknown` | Implicit (fallback path) |

The multi-tenant relay/agent architecture (`internal/tunnel`, `cmd/agent`)
is also live-verified: an agent deployed into Kind successfully connected
outbound to a locally-running backend and correctly relayed a real
troubleshoot job end to end, including surviving a pod restart/reconnect.

**Package-level test status** (unit tests, run in a network-restricted
sandbox — every package with no external dependency is fully verified;
`collector`/`k8s`/`store`/`api`/`tunnel`/`cmd/*` need `go mod tidy` on a
machine with normal network access, then build clean):

| Package | Tests |
|---|---|
| `internal/config` | 10 |
| `internal/detect` | 18 (12 failure types, including 2 real-bug regression tests found via live testing) |
| `internal/redact` | 7 |
| `internal/ai` | 4 |

## Setup (macOS, Apple Silicon)

```bash
brew install go        # verify: go version (want darwin/arm64)
cd backend-go
go get k8s.io/client-go@v0.31.0 k8s.io/api@v0.31.0 k8s.io/apimachinery@v0.31.0
go get github.com/jackc/pgx/v5@latest golang.org/x/sync@latest github.com/gorilla/websocket@latest
go mod tidy
gofmt -l .              # should print nothing
go vet ./...
go build ./...
go test ./... -v
```

## Running locally against Kind

```bash
kind create cluster --name kubeassist-dev
kubectl create namespace kubeassist
kubectl apply -f k8s/rbac/rbac.yaml

export KUBECONFIG=~/.kube/config
export KUBE_CONTEXT=kind-kubeassist-dev
export PORT=8080
# All of these are OPTIONAL — omit to run in degraded/single-cluster mode:
#   DATABASE_URL              investigation history (Postgres)
#   AI_BASE_URL / AI_AUTH_TOKEN   real AI diagnosis (else: detector-only stub)
#   AGENT_TOKENS               remote-cluster relay, e.g. "cluster1:token1,cluster2:token2"
#   CORS_ALLOWED_ORIGINS       defaults to localhost:5173,localhost:3000
go run ./cmd/server
```

Test failure scenarios are in `k8s/lab-examples/` — apply any of them and
troubleshoot the resulting pod:

```bash
kubectl apply -f k8s/lab-examples/crashloop-test.yaml
kubectl apply -f k8s/lab-examples/oom-test.yaml
kubectl apply -f k8s/lab-examples/imagepull-test.yaml
kubectl apply -f k8s/lab-examples/init-container-failure-test.yaml
kubectl apply -f k8s/lab-examples/volume-mount-failure-test.yaml

curl -X POST http://localhost:8080/api/pods/default/<pod-name>/troubleshoot
```

## Running the full stack via Docker Compose

```bash
docker compose up -d postgres
export DATABASE_URL="postgres://kubeassist:kubeassist_dev_password@localhost:5432/kubeassist?sslmode=disable"
go run ./cmd/server
```

## Remote-cluster relay (multi-tenant "bring your own cluster")

See `internal/tunnel` and `cmd/agent`. An agent runs *inside* a customer's
cluster and connects *outbound* to this backend — the customer's cluster
never needs to accept inbound traffic, and their credentials never leave
their own cluster. Full trust-model discussion (what RBAC guarantees vs.
what depends on trusting the agent's code) belongs in a follow-up
`docs/TRUST_MODEL.md` — not yet written.

```bash
docker build -f Dockerfile.agent -t kubeassist-agent:dev .
kind load docker-image kubeassist-agent:dev --name kubeassist-dev
# Edit k8s/lab-examples/agent-test.yaml if your RELAY_URL/CLUSTER_ID/AGENT_TOKEN differ
kubectl apply -f k8s/lab-examples/agent-test.yaml
```

## Testing

```bash
go test ./... -v
go vet ./...
gofmt -l .
```

## Production deployment notes (not yet done, documented for later)

- **Agent distribution**: currently requires `git clone` + `docker build`.
  For real users, publish multi-arch images to a registry (GHCR is free
  for public repos) via CI on version tags, so `docker pull` replaces
  building from source.
- **TLS**: `RELAY_PUBLIC_URL` defaults to `ws://localhost:8080/...` for
  local testing. In production this MUST become
  `wss://yourdomain.com/agent/connect` — plain `ws://` sends the agent's
  bearer token in cleartext over the internet. `cmd/agent`'s dialer
  (`gorilla/websocket`) already handles both schemes transparently — this
  is a config/infra change (terminate TLS via a reverse proxy like Caddy
  or a managed load balancer), not a code change.
- **CORS**: set `CORS_ALLOWED_ORIGINS` to your real frontend's origin,
  not the localhost dev-server defaults.
- **Supply-chain trust**: see the trust-model discussion — publishing
  checksums and/or signing images (cosign/Sigstore) with the agent
  release is the concrete next step if this is ever meant for strangers
  to run.

## Known gaps

- `internal/api`'s remote-cluster path (`handleRemoteTroubleshoot`) does
  not yet run pass-2 `CollectRelated` — would need a second tunnel round
  trip.
- No signed/checksummed releases for `cmd/agent` — see the trust-model
  discussion above before asking anyone outside your own testing to run
  it.
- `k8s/rbac/rbac.yaml`'s ConfigMap read grant is broader than strictly
  necessary (whole-object read, since Kubernetes RBAC can't scope to
  individual keys) — documented tradeoff, not a bug.
- `GET /api/investigations` has no per-user scoping — in a genuine
  multi-tenant deployment, any authenticated API caller can list ANY
  customer's saved investigation history. This is the same class of bug
  `Hub.VerifyToken` fixed for `/api/remote/...`, not yet fixed here.
- No real user accounts/login at all — `POST /api/clusters` lets anyone
  claim any unclaimed `clusterId` string with no ownership tied to an
  actual user identity. Fine for a single-operator portfolio demo;
  would need real authentication before onboarding unrelated strangers.

## Multi-tenant deployment checklist

If deploying this so that customers connect their OWN clusters (rather
than everyone sharing the operator's cluster), set:

```bash
export ENABLE_LOCAL_MODE=false   # hides /api/pods, /api/namespaces, /api/cluster,
                                  # and local /troubleshoot — your own cluster is
                                  # never reachable through the public API
export ENABLE_RELAY=true         # turns on /api/clusters registration + /api/remote/...
export RELAY_PUBLIC_URL=wss://yourdomain.com/agent/connect
```

Every `/api/remote/{clusterId}/...` call now requires the caller to send
`Authorization: Bearer <token>` — the same token returned once at
registration time via `POST /api/clusters`. Without this header (or with
another customer's token), the request is rejected with `401
invalid_cluster_token` — see `tunnel.Hub.VerifyToken` and its tests in
`internal/tunnel/hub_test.go` for the cross-customer isolation guarantee
this provides.
