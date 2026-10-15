# KubeAssist AI service (Python RAG)

Reads a **DiagnosticSnapshot** (`schemaVersion` 1.1.0) as if Go `POST /ai/troubleshoot`, retrieves local doc chunks, and returns a **DiagnosisResponse**. `failureType` is always echoed from `detector.failureType`. Default `LLM_MOCK=true` uses a schema-valid stub plus retrieved citations (no API key).

JSON schemas are not modified here. Contracts: `../schemas/`.

## Setup

```powershell
cd ai-service
python -m venv .venv
.\.venv\Scripts\Activate.ps1
pip install -r requirements.txt
python scripts\build_index.py
```

## Run

```powershell
$env:PYTHONPATH = "."
$env:LLM_MOCK = "true"
uvicorn app.main:app --host 127.0.0.1 --port 8000
```

## Simulate a Go POST

With the server running:

```powershell
python scripts\simulate_go_post.py crashloop-app.json
python scripts\simulate_go_post.py oom-failure.json
python scripts\simulate_go_post.py image-pull-failure.json
```

Fixtures: `tests/fixtures/snapshots/` (schema-valid stand-ins until live Go exists).

Auth header: `Authorization: Bearer dev-shared-token-change-me` (see `.env.example`).

## Tests

```powershell
cd ai-service
$env:PYTHONPATH = "."
pytest -q
```
