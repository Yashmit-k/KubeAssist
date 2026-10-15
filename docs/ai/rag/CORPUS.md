# Curated Kubernetes documentation corpus (pre-RAG)

This list is the **only** documentation set MVP RAG should ingest. Do not crawl all of kubernetes.io.

## License

See [DOCS_LICENSE.md](DOCS_LICENSE.md). Keep attribution; do not claim ownership of upstream docs.

## Ingest parameters (locked)

| Setting | Value |
| --- | --- |
| Chunk size | ~800–1000 tokens |
| Overlap | ~100–150 tokens |
| Embeddings | OpenAI-compatible `text-embedding-3-small` (or local compatible model) |
| Index | FAISS IndexFlatIP + L2-normalized vectors |
| Metadata per chunk | `title`, `url`, `section`, `k8s_version`, `doc_type`, `failure_tags` |
| Version filter | Prefer chunks matching cluster `serverVersion` major.minor; fallback to tagged `stable` |
| Update | Offline rebuild via future `ai-service/scripts/build_index.py` — no live crawl at request time |

## Pages to download (stable URLs)

Map each lab `failureType` to at least one page. Prefer current release docs (pin version when downloading, e.g. `/docs/concepts/...` from a chosen release tag).

| failure_tags | Suggested official topics (kubernetes.io) |
| --- | --- |
| CrashLoopBackOff, Healthy | Pods, Pod lifecycle, Debug Running Pods |
| ProbeFailure | Configure Liveness, Readiness and Startup Probes |
| ImagePullBackOff | Images, Pull an Image from a Private Registry |
| OOMKilled | Resource Management for Pods and Containers, Assign Memory Resources to Containers and Pods |
| CreateContainerConfigError | Configure a Pod to Use a ConfigMap, Secrets (concepts only — no secret values in index needed) |
| FailedScheduling | Kubernetes Scheduler, Assign CPU Resources, Pod Priority / pending troubleshooting |
| ServiceSelectorMismatch | Service, Endpoints, Debug Services |

Exact paths change by version; when implementing ingest, record the **final URL + version** in chunk metadata and in a manifest file (e.g. `ai-service/data/raw_docs/manifest.json`).

## Retrieval smoke queries (acceptance before wiring LLM)

Use golden `searchTerms` from [`docs/golden/`](../golden/):

| Query intent | Must retrieve docs about |
| --- | --- |
| liveness probe CrashLoop | Probes |
| ImagePullBackOff invalid registry | Images / pull |
| OOMKilled memory limit | Memory resources |
| FailedScheduling resource requests | Scheduling / resources |
| Service selector endpoints empty | Service / Endpoints |
| CreateContainerConfigError ConfigMap key | ConfigMaps |

## Parallel implementation note

You may build ingest + FAISS + retrieval tests **before** Go exists, using `searchTerms` from golden JSON. The troubleshoot LLM path should still receive a full `DiagnosticSnapshot` (fixture or live) with `detector` filled — use golden-shaped mock snapshots that validate against [`schemas/diagnostic-snapshot.schema.json`](../../schemas/diagnostic-snapshot.schema.json).
