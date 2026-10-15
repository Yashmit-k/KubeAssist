from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from app.paths import INDEX_DIR, RAW_DOCS_DIR
from app.rag.embed import VECTOR_DIM, embed_text

FRONTMATTER_RE = re.compile(r"^---\n(.*?)\n---\n(.*)$", re.S)
SECTION_RE = re.compile(r"^## +(.+)$", re.M)
TAGS_LINE_RE = re.compile(r"^tags:\s*\[(.*?)\]\s*$", re.M)


@dataclass
class Chunk:
    title: str
    url: str
    section: str
    k8s_version: str
    doc_type: str
    failure_tags: list[str]
    text: str


def _parse_list(val: str) -> list[str]:
    return [x.strip() for x in val.split(",") if x.strip()]


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
        key, val = key.strip(), val.strip()
        if val.startswith("[") and val.endswith("]"):
            meta[key] = _parse_list(val[1:-1])
        else:
            meta[key] = val
    return meta, m.group(2).strip()


def split_sections(body: str) -> list[tuple[str, list[str], str]]:
    """Split a document on '## Heading' lines. Each section becomes one
    chunk, so a citation points at a specific topic rather than a whole
    page. A 'tags: [...]' line directly under a heading narrows that
    section's failure tags. Text before the first heading (or a document
    without headings) is a single 'overview' section."""
    matches = list(SECTION_RE.finditer(body))
    if not matches:
        return [("overview", [], body.strip())]
    out: list[tuple[str, list[str], str]] = []
    preamble = body[: matches[0].start()].strip()
    if preamble:
        out.append(("overview", [], preamble))
    for i, m in enumerate(matches):
        end = matches[i + 1].start() if i + 1 < len(matches) else len(body)
        text = body[m.end() : end].strip()
        tags: list[str] = []
        tm = TAGS_LINE_RE.match(text)
        if tm:
            tags = _parse_list(tm.group(1))
            text = text[tm.end() :].strip()
        if text:
            out.append((m.group(1).strip(), tags, text))
    return out


def load_chunks(docs_dir: Path = RAW_DOCS_DIR) -> list[Chunk]:
    out: list[Chunk] = []
    for path in sorted(docs_dir.glob("*.md")):
        meta, body = parse_markdown(path)
        doc_tags = meta.get("failure_tags") or []
        if isinstance(doc_tags, str):
            doc_tags = [doc_tags]
        for section, sec_tags, text in split_sections(body):
            out.append(
                Chunk(
                    title=str(meta.get("title", path.stem)),
                    url=str(meta.get("url", "https://kubernetes.io/docs/")),
                    section=section,
                    k8s_version=str(meta.get("k8s_version", "1.31")),
                    doc_type=str(meta.get("doc_type", "concept")),
                    failure_tags=list(sec_tags or doc_tags),
                    text=text,
                )
            )
    return out


def build_index(docs_dir: Path = RAW_DOCS_DIR, out_dir: Path = INDEX_DIR) -> int:
    chunks = load_chunks(docs_dir)
    if not chunks:
        raise RuntimeError(f"no markdown docs in {docs_dir}")
    # Title and section are embedded with the text: headings carry a lot
    # of the topical signal for short chunks.
    mat = np.stack(
        [embed_text(f"{c.title} {c.section} {c.text} {' '.join(c.failure_tags)}") for c in chunks]
    )
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
        json.dumps({"dim": VECTOR_DIM, "count": len(chunks), "backend": "hashed-ngram-ip"}, indent=2),
        encoding="utf-8",
    )
    return len(chunks)
