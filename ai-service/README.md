# ai-service

Python FastAPI service that turns a redacted `DiagnosticSnapshot` into a `DiagnosisResponse` (schema 1.1.0). It never classifies: `failureType` always comes from the Go detector.

## Pipeline

`format_evidence` → **retrieve** (hashed n-gram vectors over the section-chunked corpus in `data/raw_docs`, boosted by the detector's failure tag, max two chunks per page) → **prompt** (evidence + docs + exact output shape) → **LLM** (any OpenAI-compatible `/chat/completions`) → **shape** (pin failureType/schemaVersion, drop unknown keys and invented citations, force `isWriteAction` on mutating commands) → **validate** (JSON Schema + grounding: specific names/numbers in evidence must exist in the snapshot) → **confidence** (`0.5·detector + 0.3·retrieval + 0.2·LLM self-score`, capped for Unknown and for missing logs where logs should exist).

If no LLM is configured (`LLM_MOCK=true` or no key), or the LLM fails or its answer fails validation, the response is the curated playbook for that failure type with real retrieved citations and `mode: "mock_llm"` — plus an uncertainty note saying why.

## Endpoints

- `GET /health` — `{"status","indexReady","chunks","llm"}`
- `POST /ai/troubleshoot` — bearer `AI_SHARED_TOKEN`; 400 on schema-invalid snapshots
- `GET /ai/doc-search?q=&failureType=&topK=` — inspect retrieval

## Run

```bash
python3 -m venv .venv && .venv/bin/pip install -r requirements-dev.txt
.venv/bin/python -m pytest -q
.venv/bin/uvicorn app.main:app --reload --port 8000
```

The index builds on startup if missing (`python scripts/build_index.py` to rebuild after editing the corpus). Schemas are read from `../schemas` (or `SCHEMAS_DIR`). Docker: `docker build -f ai-service/Dockerfile .` from the repo root.

Design history and decisions: [`docs/ai/`](../docs/ai/).
