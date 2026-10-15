from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from app.paths import INDEX_DIR
from app.rag.embed import embed_query

# Hybrid score = semantic similarity + a bonus when the chunk is tagged
# with the detector's failure type. The detector's classification is
# deterministic and trusted, so it is a strong relevance signal — much
# stronger than hashed n-gram similarity on its own.
TAG_BONUS = 0.25
MAX_CHUNKS_PER_URL = 2


@dataclass
class Retrieved:
    title: str
    url: str
    section: str
    snippet: str
    score: float
    failure_tags: list[str]


class Retriever:
    def __init__(self, index_dir: Path | None = None) -> None:
        self.index_dir = index_dir or INDEX_DIR
        self.vectors: np.ndarray | None = None
        self.meta: list[dict[str, Any]] = []

    @property
    def ready(self) -> bool:
        return self.vectors is not None and len(self.meta) > 0

    def load(self) -> None:
        vec_path = self.index_dir / "vectors.npy"
        meta_path = self.index_dir / "metadata.json"
        if not vec_path.exists() or not meta_path.exists():
            raise FileNotFoundError(
                f"index files missing in {self.index_dir}. Run: python scripts/build_index.py"
            )
        self.vectors = np.load(vec_path)
        self.meta = json.loads(meta_path.read_text(encoding="utf-8"))

    def search(
        self,
        search_terms: list[str],
        failure_type: str,
        user_question: str = "",
        top_k: int = 5,
        server_version: str = "",
    ) -> list[Retrieved]:
        if self.vectors is None:
            self.load()
        assert self.vectors is not None
        q = embed_query([failure_type, failure_type, user_question, *search_terms])
        sims = self.vectors @ q
        major_minor = _major_minor(server_version)

        scored: list[Retrieved] = []
        for i, sim in enumerate(sims.tolist()):
            row = self.meta[i]
            tags = list(row.get("failure_tags") or [])
            # Map cosine [-1, 1] onto [0, 1] so scores compose with the
            # confidence formula, then add the tag bonus.
            score = (float(sim) + 1.0) / 2.0
            if failure_type and failure_type in tags:
                score += TAG_BONUS
            # Slight preference for docs written against the cluster's
            # own minor version, when we know it.
            ver = str(row.get("k8s_version", ""))
            if major_minor and ver.startswith(major_minor):
                score += 0.02
            scored.append(
                Retrieved(
                    title=row["title"],
                    url=row["url"],
                    section=row.get("section", ""),
                    snippet=row["text"][:500],
                    score=round(min(1.0, score), 4),
                    failure_tags=tags,
                )
            )

        scored.sort(key=lambda h: h.score, reverse=True)
        hits: list[Retrieved] = []
        per_url: dict[str, int] = {}
        for h in scored:
            if per_url.get(h.url, 0) >= MAX_CHUNKS_PER_URL:
                continue
            per_url[h.url] = per_url.get(h.url, 0) + 1
            hits.append(h)
            if len(hits) >= top_k:
                break
        return hits


def _major_minor(server_version: str) -> str:
    m = re.search(r"v?(\d+\.\d+)", server_version or "")
    return m.group(1) if m else ""
