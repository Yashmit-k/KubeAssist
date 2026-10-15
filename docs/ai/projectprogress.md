# Project progress log

Running log of what landed, in order. High-level roadmap: [PROJECT_STATUS.md](PROJECT_STATUS.md). Decisions: [decision.md](decision.md).

Append a new dated section when a slice of work finishes. Do not delete old entries.

---

## 2026-08 — Foundations (before RAG service)

- Kind lab manifests: 8 scenarios under `k8s/lab/` (healthy, crashloop, probe, image pull, OOM, missing config, scheduling, service selector).
- Read-only RBAC under `k8s/rbac/`.
- Architecture locked: evidence first, two-pass collector, detectors in Go, LLM explains only.
- Golden **notes** (not snapshots) in `docs/golden/`.
- Implementation plan: `docs/IMPLEMENTATION_PLAN.md` (also Cursor plan `kubeassist_implementation_plan_57570db3`).
- Shared schemas introduced as 1.0.0 then **bumped to 1.1.0** to match Go detector JSON (flattened container state, `initContainers`, `endpointsReady`, extra failure types). See D2, D3.

**Not done:** live Kind verification against every golden; `backend-go/`; frontend; Postgres.

---

## 2026-08-14 — Contract alignment (RAG ↔ Go)

- Reviewed Go `ai.Client` (POST snapshot, retries) and `detect` package.
- Agreed: extra types stay in the enum; `ErrImagePull` is not a type.
- Discussed then **deferred** “ask Go to reclassify” (D1, D4).
- Schema files on disk: `schemaVersion` `1.1.0` for snapshot + diagnosis. `schemas/README.md` may still mention 1.0.0 (docs drift).

---

## 2026-08-16 — Python RAG MVP (`ai-service/`)

**Schemas:** unchanged (kept tight per D4).

**Shipped:**

| Piece | Location |
| --- | --- |
| FastAPI app | `ai-service/app/main.py` — `GET /health`, `POST /ai/troubleshoot` |
| Snapshot/diagnosis JSON Schema validation | `ai-service/app/schema_util.py` |
| Evidence formatter, prompt, stub playbook, hybrid confidence, grounding validator | `ai-service/app/pipeline/` |
| Hashed local index + retriever | `ai-service/app/rag/`, `scripts/build_index.py` |
| Curated excerpt corpus | `ai-service/data/raw_docs/` |
| Simulated Go POSTs | `ai-service/tests/fixtures/snapshots/`, `scripts/simulate_go_post.py` |
| Tests | 9 passed (`pytest`): fixtures validate, retrieval smoke, troubleshoot echoes type, HTTP POST |

**How to run:** [ai-service/README.md](../ai-service/README.md).

**Defaults:** `LLM_MOCK=true`; bearer `AI_SHARED_TOKEN`. Live LLM optional (D8, D9).

**Explicitly not in this slice:** recollect handshake, OpenAI embeddings, faiss-cpu, Go collector, React, SSE, Postgres.

---

## Next (ordered)

1. Go owner: POST real 1.1.0 snapshots (JSON tags = schema); detector fixes (Healthy-only `DetectRelated`, Pending gate, never emit `ErrImagePull` as type).
2. Optional: swap hashed vectors for embeddings + FAISS without changing the HTTP contract.
3. Optional schema 1.2: `needsRecollect` / `recollectHints` if both owners agree (D4).
4. Phase 6–7: frontend, Go orchestrator, compose, history DB.
5. Kind eval: live snapshots vs golden notes.

---

## Changelog template (copy for the next entry)

```
## YYYY-MM-DD — <title>

- What shipped (files / behavior)
- Tests / how to verify
- Decisions added or changed (D#)
- What is still blocked
```
