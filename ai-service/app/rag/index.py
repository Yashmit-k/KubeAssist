from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from app.paths import FAISS_DIR, RAW_DOCS_DIR
from app.rag.embed import VECTOR_DIM, embed_text

FRONTMATTER_RE = re.compile(r"^---\n(.*?)\n---\n(.*)$", re.S)


@dataclass
class Chunk:
    title: str
    url: str
    section: str
    k8s_version: str
    doc_type: str
    failure_tags: list[str]
    text: str


def parse_markdown(path: Path) -> tuple[dict[str, Any], str]:
    raw = path.read_text(encoding="utf-8")
    m = FRONTMATTER_RE.match(raw)
    if not m:
        raise ValueError(f"missing frontmatter: {path}")
    meta: dict[str, Any] = {}
    for line in m.group(1).splitlines():
        if ":" not in line:
            continue
        key, val = line.split(":", 1)
        key = key.strip()
        val = val.strip()
        if val.startswith("[") and val.endswith("]"):
            inner = val[1:-1].strip()
            meta[key] = [x.strip() for x in inner.split(",") if x.strip()]
        else:
            meta[key] = val
    return meta, m.group(2).strip()


def chunk_text(text: str, max_chars: int = 3200, overlap: int = 400) -> list[str]:
    paras = [p.strip() for p in re.split(r"\n\n+", text) if p.strip()]
    chunks: list[str] = []
    buf = ""
    for p in paras:
        if buf and len(buf) + len(p) + 2 > max_chars:
            chunks.append(buf.strip())
            buf = buf[-overlap:] + "\n\n" + p
        else:
            buf = f"{buf}\n\n{p}" if buf else p
    if buf.strip():
        chunks.append(buf.strip())
    return chunks or [text]


def load_chunks(docs_dir: Path = RAW_DOCS_DIR) -> list[Chunk]:
    out: list[Chunk] = []
    for path in sorted(docs_dir.glob("*.md")):
        meta, body = parse_markdown(path)
        tags = meta.get("failure_tags") or []
        if isinstance(tags, str):
            tags = [tags]
        for i, piece in enumerate(chunk_text(body)):
            section = str(meta.get("section", "body"))
            if i:
                section = f"{section}#{i + 1}"
            out.append(
                Chunk(
                    title=str(meta.get("title", path.stem)),
                    url=str(meta.get("url", "https://kubernetes.io/docs/")),
                    section=section,
                    k8s_version=str(meta.get("k8s_version", "1.31")),
                    doc_type=str(meta.get("doc_type", "concept")),
                    failure_tags=list(tags),
                    text=piece,
                )
            )
    return out


def build_index(docs_dir: Path = RAW_DOCS_DIR, out_dir: Path = FAISS_DIR) -> int:
    chunks = load_chunks(docs_dir)
    if not chunks:
        raise RuntimeError(f"no markdown docs in {docs_dir}")
    mat = np.stack([embed_text(c.text + " " + " ".join(c.failure_tags)) for c in chunks])
    meta = [
        {
            "title": c.title,
            "url": c.url,
            "section": c.section,
            "k8s_version": c.k8s_version,
            "doc_type": c.doc_type,
            "failure_tags": c.failure_tags,
            "text": c.text,
        }
        for c in chunks
    ]
    out_dir.mkdir(parents=True, exist_ok=True)
    np.save(out_dir / "vectors.npy", mat)
    (out_dir / "metadata.json").write_text(json.dumps(meta, indent=2), encoding="utf-8")
    (out_dir / "manifest.json").write_text(
        json.dumps({"dim": VECTOR_DIM, "count": len(chunks), "backend": "hashed-ip"}, indent=2),
        encoding="utf-8",
    )
    return len(chunks)
