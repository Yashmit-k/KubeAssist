from __future__ import annotations

from typing import Any

from app.rag.retrieve import Retrieved
from app.schema_util import validate_diagnosis


def grounding_errors(
    diagnosis: dict[str, Any],
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
) -> list[str]:
    errs: list[str] = []
    want = (snapshot.get("detector") or {}).get("failureType")
    if diagnosis.get("failureType") != want:
        errs.append(f"failureType {diagnosis.get('failureType')!r} != detector {want!r}")
    allowed = {d.url for d in retrieved}
    for ref in diagnosis.get("documentationReferences") or []:
        url = ref.get("url")
        if url and allowed and url not in allowed:
            errs.append(f"citation url not in retrieval: {url}")
    blob = _blob(snapshot)
    for bullet in diagnosis.get("observedEvidence") or []:
        if not _loosely_grounded(bullet, blob, snapshot):
            errs.append(f"evidence not grounded in snapshot: {bullet[:80]}")
    return errs


def validate_full(
    diagnosis: dict[str, Any],
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
) -> list[str]:
    return validate_diagnosis(diagnosis) + grounding_errors(diagnosis, snapshot, retrieved)


def _blob(snapshot: dict[str, Any]) -> str:
    return str(snapshot).lower()


def _loosely_grounded(bullet: str, blob: str, snapshot: dict[str, Any]) -> bool:
    b = bullet.lower()
    tokens = [t for t in b.replace(",", " ").split() if len(t) > 4]
    if any(t in blob for t in tokens[:8]):
        return True
    det = snapshot.get("detector") or {}
    for ev in det.get("evidence") or []:
        if ev.lower()[:40] in b or b[:40] in ev.lower():
            return True
    return False
