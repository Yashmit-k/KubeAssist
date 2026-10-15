from __future__ import annotations

from typing import Any

from fastapi import APIRouter, Depends, Header, HTTPException

from app.config import settings
from app.pipeline.troubleshoot import TroubleshootError, run_troubleshoot
from app.schema_util import validate_snapshot
from app.state import retriever

router = APIRouter()


def _auth(authorization: str | None = Header(default=None)) -> None:
    if not settings.shared_token:
        return
    expected = f"Bearer {settings.shared_token}"
    if authorization != expected:
        raise HTTPException(status_code=401, detail="unauthorized")


@router.get("/health")
def health() -> dict[str, Any]:
    return {"status": "ok", "indexReady": retriever.ready}


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
