# KubeAssist AI — Architecture Decisions

This document locks design decisions that fix earlier weak spots. Implementers (Go, AI, frontend) must follow these rules.

---

## Correctness rules

1. **Evidence first.** Every diagnosis is grounded in a live (or fixture) `DiagnosticSnapshot`. The LLM must not invent cluster facts.
2. **Detectors classify; LLM explains.** Go detectors set `failureType`, `evidence`, and `searchTerms`. The Python AI service **must not** re-classify failure type. It may only explain, remediate, and cite docs.
3. **Read-only.** No auto-remediation, no kubectl execution, no operators.
4. **Secrets never leave the cluster path unredacted.** Secret *values*, tokens, PEM blocks, and password-like strings are stripped before any payload is sent to the AI service.

---

## Trust boundaries

```
React  -->  Go API  -->  Kubernetes API (kubeconfig / SA)
                |
                +-->  redacted DiagnosticSnapshot  -->  Python AI (FAISS + LLM)
                |
                +-->  PostgreSQL (investigation history)
```

- Only Go holds kubeconfig / in-cluster credentials.
- Python receives JSON only; never kube credentials.
- FAISS index is local files under `ai-service/data/faiss/` (built offline).

---

## Collector: two-pass (not always-fetch-everything)

**Pass 1 — core (always):**

- Pod status, conditions, container states, restart counts, waiting/termination reasons
- Resource requests/limits, probe summaries
- Events (capped)
- Current logs (truncated); previous logs **only if** `restartCount > 0`
- Owner chain: Pod → ReplicaSet → Deployment (summaries + sanitized YAML excerpts)

**Pass 2 — expand when detectors / hints require it:**

| Hint / failureType | Extra fetch |
| --- | --- |
| `ServiceSelectorMismatch` or related Service | Services in namespace, Endpoints / EndpointSlices |
| `CreateContainerConfigError` | ConfigMap **names/keys** (not Secret values) |
| User passes `serviceName` | That Service + endpoints |

**Out of MVP unless a golden case needs them:** Ingress, NetworkPolicy, Prometheus metrics.

### Caps (mandatory)

| Field | Cap |
| --- | --- |
| Events | Max 30, newest first |
| Current / previous logs | Max 200 lines **or** 32 KiB (whichever first) |
| YAML excerpts | Sanitized; no `data:` secret values |

### Phase vs Running

| Pod phase | Collector behavior |
| --- | --- |
| `Pending` | Emphasize scheduling events, resource requests, node affinity; logs often empty — mark `logsUnavailable` |
| `Running` / `Succeeded` / `Failed` | Full container status + logs as applicable |
| Waiting reasons present | Prefer container `state.waiting.reason` over guessing from logs alone |

---

## Failure taxonomy (detectors in Go)

| failureType | Lab fixture | Notes |
| --- | --- | --- |
| `Healthy` | `healthy-app` | Control |
| `CrashLoopBackOff` | `crashloop-app` | App exit; no probe Unhealthy as primary cause |
| `ProbeFailure` | `probe-failure-app` | Prefer over CrashLoop when Unhealthy events + probe config |
| `ImagePullBackOff` | `image-pull-failure` | Includes ErrImagePull |
| `OOMKilled` | `oom-failure` | termination reason OOMKilled |
| `CreateContainerConfigError` | `missing-config-app` | Missing ConfigMap/Secret key |
| `FailedScheduling` | `scheduling-failure` | Pending + FailedScheduling |
| `ServiceSelectorMismatch` | `service-selector-failure` | Pod may be Healthy; Service has 0 endpoints |
| `Unknown` | — | Low confidence; LLM must state uncertainty |

**Service entrypoint:** UI/API may pass optional `serviceName`. If omitted, pass-2 lists Services in the namespace and flags selector/endpoint mismatches related to the pod’s labels.

---

## AI / RAG constraints

| Decision | Rule |
| --- | --- |
| Phase order | **Docs index (FAISS) before** full troubleshoot RAG path |
| LangChain | Optional for loaders/splitters only. No agents. Prefer FastAPI + OpenAI-compatible SDK + custom prompt/validator |
| Streaming | SSE **stage** events: `collecting` → `detecting` → `retrieving` → `diagnosing` → `done`. Final body is full JSON. Token streaming is optional polish |
| Infra | PostgreSQL for history. **No Redis. No Kafka.** Go in-process TTL cache (5–15s) for pod list only |
| Offline / no LLM | `LLM_MOCK=true` or missing key → detectors-only stub diagnosis (still schema-valid) for demos/CI |
| Parallel work | AI can be built against `schemas/` + `docs/golden/` fixtures **without** waiting for Go, if the snapshot contract is not changed unilaterally |

Shared contracts: [`schemas/`](../schemas/). Curated corpus list: [`docs/rag/CORPUS.md`](rag/CORPUS.md).

---

## Diagnosis output (required fields)

Every diagnosis must include:

- Issue Summary  
- Observed Evidence  
- Probable Root Cause  
- Reasoning  
- Confidence Score  
- Step-by-Step Resolution  
- Verification Commands  
- Rollback Guidance  
- Official Documentation References  
- Uncertainty notes when evidence is missing  

See [`schemas/diagnosis.schema.json`](../schemas/diagnosis.schema.json).

---

## Corrected phase order

```
1 Lab verification + golden notes
2 Go collector + snapshot + redaction
3 Deterministic detectors (Go)
4 Documentation ingest + FAISS index
5 Python AI + RAG troubleshoot pipeline
6 React frontend
7 Integration (Go ↔ Python ↔ Postgres ↔ SSE)
8 Evaluation + polish
```

Do **not** build a troubleshoot LLM path before an index exists (unless using mock retrieval for unit tests).

---

## Security summary

- RBAC: [`k8s/rbac/`](../k8s/rbac/) — read-only; no Secrets verb
- Commands suggested by AI are **manual copy-paste only**
- Python must not fetch arbitrary URLs (no SSRF)
