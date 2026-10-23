from __future__ import annotations

import hmac
from typing import Any

from fastapi import APIRouter, Depends, Header, HTTPException, Query

from app.chat.service import ChatError, chat_step
from app.config import settings
from app.pipeline.troubleshoot import TroubleshootError, run_troubleshoot
from app.schema_util import validate_snapshot
from app.state import retriever

router = APIRouter()


def _auth(authorization: str | None = Header(default=None)) -> None:
    if not settings.shared_token:
        return
    expected = f"Bearer {settings.shared_token}"
    # Constant-time comparison: this is a shared secret.
    if not authorization or not hmac.compare_digest(authorization.encode(), expected.encode()):
        raise HTTPException(status_code=401, detail="unauthorized")


@router.get("/health")
def health() -> dict[str, Any]:
    return {
        "status": "ok",
        "indexReady": retriever.ready,
        "chunks": len(retriever.meta),
        "llm": "mock" if settings.use_mock_llm else settings.llm_model,
    }


@router.post("/ai/troubleshoot")
async def troubleshoot(
    snapshot: dict[str, Any],
    _: None = Depends(_auth),
) -> dict[str, Any]:
    errs = validate_snapshot(snapshot)
    if errs:
        raise HTTPException(status_code=400, detail={"code": "invalid_snapshot", "errors": errs[:12]})
    try:
        return await run_troubleshoot(snapshot, retriever)
    except TroubleshootError as e:
        raise HTTPException(status_code=500, detail={"code": "diagnosis_invalid", "message": str(e)}) from e


@router.post("/ai/chat")
async def chat(req: dict[str, Any], _: None = Depends(_auth)) -> dict[str, Any]:
    """One step of the cluster chat loop (the Go backend runs the loop
    and the read-only tools)."""
    try:
        return await chat_step(req)
    except ChatError as e:
        raise HTTPException(status_code=400, detail={"code": "invalid_chat", "message": str(e)}) from e


@router.get("/ai/doc-search")
def doc_search(
    q: str = Query(default="", max_length=500),
    failureType: str = Query(default=""),
    topK: int = Query(default=5, ge=1, le=20),
    _: None = Depends(_auth),
) -> list[dict[str, Any]]:
    """Debug/inspection endpoint: what would retrieval return?"""
    hits = retriever.search([], failureType, user_question=q, top_k=topK)
    return [
        {"title": h.title, "url": h.url, "section": h.section, "score": h.score, "failureTags": h.failure_tags, "snippet": h.snippet}
        for h in hits
    ]
