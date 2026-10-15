# Design decisions

This file records **why** we chose something, not a full architecture dump. Locked rules also live in [ARCHITECTURE.md](ARCHITECTURE.md). Contracts: [`schemas/`](../schemas/).

When you change a decision, add a new dated entry (do not silently rewrite history). Mark superseded items as **Superseded**.

---

## D1 — Go classifies; Python explains (not the other way around)

**Date:** 2026-08  
**Status:** Active  

Go detectors set `detector.failureType` with if/else on the snapshot. Python **echoes** that value on `DiagnosisResponse.failureType`.

**Why:** The same snapshot must always get the same type so tests work. An LLM that “fixes” a wrong class is hard to debug and can send someone down the wrong fix path. Speed: no LLM round-trip just to know OOMKilled.

**Rejected:** RAG reclassifies when confidence is low. Low score means thin evidence, not “change the sticker.” Same snapshot → same Go type.

---

## D2 — Shared JSON Schema is the only Go ↔ Python contract

**Date:** 2026-08  
**Status:** Active (`schemaVersion` **1.1.0**)  

- Go → Python: [`diagnostic-snapshot.schema.json`](../schemas/diagnostic-snapshot.schema.json)  
- Python → Go: [`diagnosis.schema.json`](../schemas/diagnosis.schema.json)  

`additionalProperties: false`. Extra keys fail validation.

**Why:** Two people can build in parallel. Python never needs kubeconfig. If Go’s JSON tags drift, CI/schema validation fails loudly.

---

## D3 — Snapshot 1.1.0 matches Go detector field names

**Date:** 2026-08-14  
**Status:** Active  

Go used flattened `state.phase` / `reason` / `message` / `exitCode`, `initContainers`, `pod.reason` / `pod.message`, `resources.limitsMemory`, string `probes.*`, `related.services[].endpointsReady`.

**Why:** The original 1.0.0 nested `state.waiting.reason` and `endpointCount` would reject live Go payloads. We aligned the schema to Go instead of asking RAG to guess both shapes.

**Not a type:** `ErrImagePull` stays in **evidence**. `failureType` is always `ImagePullBackOff`.

**Added types (were missing from the lab-8 enum):** `Evicted`, `InitContainerFailure`, `VolumeMountFailure`, `ReadinessFailure`.

---

## D4 — Keep schemas tight; no recollect fields yet

**Date:** 2026-08-16  
**Status:** Active  

A “collect more clues” loop (`needsRecollect`, `recollectHints`, `collectionPass`, `fetchedHints`) was discussed, then **not** added.

**Why:** User asked to keep the agreed 1.1.0 schemas and implement RAG against them. Recollect is a second handshake and a schema bump. Until then, missing logs → `uncertaintyNotes` + lower `confidenceScore`.

**If revisited:** Python still must not POST to Go. Go would POST a second snapshot after fetching hints. One extra round max. Not “reclassify.”

---

## D5 — Two-pass collector (Go); Python is JSON-only

**Date:** 2026-08 (architecture)  
**Status:** Active  

Pass 1: pod, events, truncated logs, owners. Pass 2: Services/endpoints, ConfigMap **names/keys** when needed. Only Go talks to Kubernetes.

**Why:** Always-fetch-everything is slow and noisy. Secrets/tokens never leave the cluster path unredacted.

---

## D6 — Hybrid confidence; Go priors are not the UI score

**Date:** 2026-08  
**Status:** Active  

```
confidenceScore = 0.5 * detector.confidence
               + 0.3 * retrievalScore
               + 0.2 * llmSelfScore
```

Cap down for `Unknown` or empty logs (except types that normally have no logs: ImagePull, FailedScheduling, VolumeMount, Healthy, ServiceSelectorMismatch).

**Why:** Go’s 0.95/0.90 values are “how sure is this rule,” not a diagnosis quality score. RAG owns retrieval + explanation quality.

---

## D7 — Curated corpus, not a kubernetes.io crawl

**Date:** 2026-08  
**Status:** Active  

Ingest list: [rag/CORPUS.md](rag/CORPUS.md). MVP index: original short summaries in `ai-service/data/raw_docs/` with **official URLs** for citations.

**Why:** Full-site crawl is huge, slow, and messy for license/attribution. Citations must still point at kubernetes.io. See [rag/DOCS_LICENSE.md](rag/DOCS_LICENSE.md).

---

## D8 — Offline hashed embeddings instead of live OpenAI embeddings / faiss-cpu

**Date:** 2026-08-16  
**Status:** Active (MVP)  

Index = L2-normalized hashed n-gram vectors + numpy inner product (`vectors.npy` + `metadata.json`). Same retrieval API as “FAISS IndexFlatIP” without a native FAISS wheel (painful on Windows) or an embedding API key.

**Why:** RAG track had to run in CI/demo with `LLM_MOCK=true`. Plan still allows swapping in `text-embedding-3-small` + FAISS later without changing `/ai/troubleshoot`.

---

## D9 — Mock LLM stub is the default path

**Date:** 2026-08-16  
**Status:** Active  

`LLM_MOCK=true` or empty `LLM_API_KEY` → playbook stub + retrieved citations, `mode: mock_llm`. Optional live OpenAI-compatible JSON completion when a key is set.

**Why:** Architecture: demos/CI must work without a billable model. Stub still **echoes** Go’s type and must pass `diagnosis.schema.json`.

---

## D10 — Simulate Go POST with schema-valid fixtures

**Date:** 2026-08-16  
**Status:** Active  

`ai-service/tests/fixtures/snapshots/` + `scripts/simulate_go_post.py`. Golden files under `docs/golden/` stay **expected notes**, not POST bodies.

**Why:** Go API is another owner. RAG cannot wait. Fixtures are the wire format 1.1.0 so Python validators stay honest.

---

## D11 — Response validator: schema + echo type + citation allowlist

**Date:** 2026-08-16  
**Status:** Active  

After JSON Schema: `failureType` must equal `detector.failureType`; citation `url` must appear in this request’s retrieval hits; `observedEvidence` must loosely match snapshot/detector text.

**Why:** Plan anti-hallucination: no invented logs, no docs the retriever did not return.

---

## D12 — Auth between Go and Python is a shared bearer token

**Date:** 2026-08 (plan)  
**Status:** Active  

`Authorization: Bearer <AI_SHARED_TOKEN>`. Enough for local docker-compose MVP.

**Why:** Python is not a public API. No OAuth for student scope.

---

## D13 — No Redis, Kafka, or Python kube client

**Date:** 2026-08  
**Status:** Active  

Postgres is for investigation **history** (Go, later). Vectors stay on disk.

**Why:** Student MVP ops budget. Extra brokers add nothing to a single-user Kind demo.

---

## D14 — Detector priority (Go; Python must understand it)

**Date:** 2026-08  
**Status:** Active (Go implements; Python does not re-order)  

More specific signatures win: Evicted → init → ImagePull → OOM → volume → config error → scheduling → ProbeFailure → CrashLoop → Readiness. `DetectRelated` (ServiceSelectorMismatch) should override **Healthy only**.

**Why:** ProbeFailure looks like CrashLoop; OOM waits as CrashLoopBackOff. Scoring “most evidence” is unstable. Python must not “correct” this order.

---

## How to add a decision

1. Next id `D15`.  
2. Date, status, one-paragraph why, what we rejected.  
3. Link the PR or files.  
4. Log the change in [projectprogress.md](projectprogress.md).
