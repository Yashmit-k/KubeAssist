from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from app.paths import FAISS_DIR
from app.rag.embed import embed_query


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
        self.index_dir = index_dir or FAISS_DIR
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
                f"FAISS/index files missing in {self.index_dir}. Run: python -m scripts.build_index"
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
        q = embed_query([failure_type, user_question, *search_terms])
        scores = self.vectors @ q
        major_minor = _major_minor(server_version)
        ranked = list(enumerate(scores.tolist()))
        ranked.sort(key=lambda x: x[1], reverse=True)
        hits: list[Retrieved] = []
        fallback: list[Retrieved] = []
        for i, sc in ranked:
            row = self.meta[i]
            item = Retrieved(
                title=row["title"],
                url=row["url"],
                section=row.get("section", ""),
                snippet=row["text"][:400],
                score=max(0.0, min(1.0, (float(sc) + 1.0) / 2.0)),
                failure_tags=list(row.get("failure_tags") or []),
            )
            if failure_type in item.failure_tags:
                item.score = min(1.0, item.score + 0.08)
            ver = str(row.get("k8s_version", ""))
            if major_minor and ver.startswith(major_minor):
                hits.append(item)
            else:
                fallback.append(item)
            if len(hits) >= top_k:
                break
        if len(hits) < top_k:
            hits.extend(fallback[: top_k - len(hits)])
        hits.sort(key=lambda h: h.score, reverse=True)
        return hits[:top_k]


def _major_minor(server_version: str) -> str:
    m = __import__("re").search(r"v?(\d+\.\d+)", server_version or "")
    return m.group(1) if m else ""
