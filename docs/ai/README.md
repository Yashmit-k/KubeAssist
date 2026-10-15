# KubeAssist AI

Kubernetes troubleshooting assistant (student MVP). Collects live cluster evidence, classifies failures with **deterministic detectors**, retrieves Kubernetes docs with RAG, and returns structured diagnoses with fix steps — without auto-applying changes.

| Doc | Purpose |
| --- | --- |
| [docs/projectprogress.md](docs/projectprogress.md) | Running progress log |
| [docs/decision.md](docs/decision.md) | Why we chose each design |
| [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md) | Status + roadmap |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Locked design decisions |
| [schemas/](schemas/) | Go ↔ AI ↔ UI contracts |
| [docs/golden/](docs/golden/) | Expected signals per lab scenario |
| [docs/rag/CORPUS.md](docs/rag/CORPUS.md) | Curated docs list before RAG ingest |
| [ai-service/README.md](ai-service/README.md) | Run the Python RAG service |

---

## Current progress

- **Done:** Lab manifests (8), architecture decisions, shared schemas 1.1.0, golden notes, read-only RBAC, RAG corpus prep, `.env.example`
- **RAG:** `ai-service/` FastAPI `/ai/troubleshoot` with local hashed index, mock-LLM diagnoses, schema-valid Go POST fixtures
- **Fixed:** `missing-config-app` now includes the broken Deployment (was ConfigMap-only)
- **Partial:** Verify every scenario on Kind against goldens; live OpenAI embeddings/LLM optional
- **Overall:** RAG MVP runnable against fixtures; Go API still separate

---

## How to run the lab

**Prerequisites:** Docker Desktop, Kind, kubectl.

```powershell
kind create cluster --name kubeassist-dev
kubectl create namespace kubeassist-lab
kubectl apply -f .\k8s\lab\
kubectl apply -f .\k8s\rbac\
kubectl get pods -n kubeassist-lab
```

Inspect a failing pod (use the real name from `get pods`, or a label):

```powershell
kubectl describe pod -l app=crashloop-app -n kubeassist-lab
kubectl logs -l app=crashloop-app -n kubeassist-lab --tail=50
```

Service mismatch (pod is healthy — check the Service):

```powershell
kubectl get endpoints broken-service -n kubeassist-lab
kubectl get svc broken-service -n kubeassist-lab -o yaml
```

Reset:

```powershell
kubectl delete namespace kubeassist-lab
kubectl create namespace kubeassist-lab
kubectl apply -f .\k8s\lab\
kubectl apply -f .\k8s\rbac\
```

Compare observed signals to [docs/golden/](docs/golden/).

---

## Design rules (do not regress)

1. Detectors (Go) set `failureType`; LLM only explains.  
2. Collector is **two-pass** (core, then related).  
3. Docs/FAISS index before full RAG troubleshoot.  
4. No Redis/Kafka; Postgres for history only; SSE for pipeline stages.  
5. Read-only RBAC; secrets never sent to the AI.

---

## What to build next

1. Finish Kind verification against golden notes.  
2. **RAG track:** ingest curated corpus → FAISS → retrieval tests (use schemas + golden `searchTerms`; mock snapshots OK).  
3. **Go track (other owner):** client-go snapshot + detectors; integrate later via schemas.  
4. Do not change [schemas/](schemas/) without agreeing across owners.

See [docs/PROJECT_STATUS.md](docs/PROJECT_STATUS.md) for the full roadmap.
