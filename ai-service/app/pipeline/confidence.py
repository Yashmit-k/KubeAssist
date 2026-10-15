from __future__ import annotations

from typing import Any

from app.rag.retrieve import Retrieved


def hybrid_confidence(
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
    llm_self: float | None,
) -> float:
    det = float((snapshot.get("detector") or {}).get("confidence") or 0.0)
    if retrieved:
        retrieval = sum(h.score for h in retrieved) / len(retrieved)
    else:
        retrieval = 0.0
    self_score = 0.7 if llm_self is None else float(llm_self)
    raw = 0.5 * det + 0.3 * retrieval + 0.2 * self_score
    ftype = (snapshot.get("detector") or {}).get("failureType")
    logs = snapshot.get("logs") or {}
    no_logs = not logs.get("current") and not logs.get("previous")
    if ftype == "Unknown":
        raw = min(raw, 0.35)
    elif no_logs and ftype not in {"ImagePullBackOff", "FailedScheduling", "VolumeMountFailure", "Healthy", "ServiceSelectorMismatch"}:
        raw = min(raw, 0.55)
    return round(max(0.0, min(1.0, raw)), 3)
