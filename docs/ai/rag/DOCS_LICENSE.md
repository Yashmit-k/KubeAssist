# Documentation corpus — license and attribution

KubeAssist AI retrieves excerpts from **official Kubernetes documentation** published at [https://kubernetes.io](https://kubernetes.io) for troubleshooting citations.

## Upstream

Kubernetes documentation is maintained by the Kubernetes project and contributors. Content is typically available under Creative Commons terms as stated on kubernetes.io (see the site footer / license pages for the version you download).

## Project obligations

1. **Do not** claim ownership of upstream documentation text.
2. When storing raw pages under `ai-service/data/raw_docs/`, keep a `manifest.json` with source URL, retrieval date, and doc version.
3. UI and API responses must show **citations** (title + URL) for retrieved chunks.
4. Prefer linking users to the official page rather than reproducing entire articles.
5. This repository’s application code remains under this project’s own license (add a root `LICENSE` when you choose one); the **corpus files** remain third-party documentation subject to upstream terms.

## MVP scope

Only the curated list in [CORPUS.md](CORPUS.md) is ingested. No bulk mirror of the entire kubernetes.io site.
