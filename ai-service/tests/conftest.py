from __future__ import annotations

import json
from pathlib import Path

import pytest

from app.paths import FIXTURES_DIR
from app.rag.index import build_index
from app.rag.retrieve import Retriever
from app.state import retriever as app_retriever


@pytest.fixture(scope="session", autouse=True)
def built_index() -> Retriever:
    build_index()
    app_retriever.load()
    return app_retriever


@pytest.fixture
def snapshots() -> dict[str, dict]:
    out = {}
    for p in Path(FIXTURES_DIR).glob("*.json"):
        out[p.stem] = json.loads(p.read_text(encoding="utf-8"))
    return out
