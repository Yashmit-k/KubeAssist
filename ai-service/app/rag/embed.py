from __future__ import annotations

import hashlib
import re
from typing import Iterable

import numpy as np

VECTOR_DIM = 384
TOKEN_RE = re.compile(r"[a-z0-9]+", re.I)


def tokenize(text: str) -> list[str]:
    return TOKEN_RE.findall(text.lower())


def embed_text(text: str, dim: int = VECTOR_DIM) -> np.ndarray:
    """Offline hashed n-gram embedding (no API). L2-normalized for inner-product search."""
    vec = np.zeros(dim, dtype=np.float32)
    tokens = tokenize(text)
    if not tokens:
        return vec
    grams: list[str] = list(tokens)
    grams.extend(f"{a}_{b}" for a, b in zip(tokens, tokens[1:]))
    for g in grams:
        h = int(hashlib.sha256(g.encode()).hexdigest(), 16)
        idx = h % dim
        sign = 1.0 if (h >> 8) & 1 else -1.0
        vec[idx] += sign
    norm = float(np.linalg.norm(vec))
    if norm > 0:
        vec /= norm
    return vec


def embed_query(parts: Iterable[str]) -> np.ndarray:
    return embed_text(" ".join(p for p in parts if p))
