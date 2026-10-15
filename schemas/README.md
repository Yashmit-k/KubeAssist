# Shared contracts (Go ↔ Python ↔ Frontend)

These JSON Schemas are the integration contract. Change them only with agreement across owners.

| Schema | File | Producer | Consumer |
| --- | --- | --- | --- |
| Diagnostic snapshot | [diagnostic-snapshot.schema.json](diagnostic-snapshot.schema.json) | Go collector (+ detectors) | Python `/ai/troubleshoot` |
| Diagnosis response | [diagnosis.schema.json](diagnosis.schema.json) | Python AI | Go API → React |

## Rules

1. `schemaVersion` is currently `1.0.0`. Bump when breaking fields change.
2. Go sets `detector.failureType` and `detector.searchTerms`. Python **echoes** `failureType` and must not invent a different classification.
3. AI development may proceed using fixtures under [`docs/golden/`](../docs/golden/) shaped like the snapshot schema — no Go required.
4. Validate fixtures and API payloads against these schemas in CI once services exist.

## Example flow

```
Go: CollectCore → Detect → CollectRelated → Redact → DiagnosticSnapshot
Python: Format evidence → Retrieve(searchTerms) → Prompt → LLM → Validate → DiagnosisResponse
Go: Persist → return to UI (optional SSE stages along the way)
```
