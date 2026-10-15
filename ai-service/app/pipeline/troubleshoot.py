from __future__ import annotations

from typing import Any

from app.config import settings
from app.llm.client import complete_json
from app.pipeline.confidence import hybrid_confidence
from app.pipeline.formatter import format_evidence
from app.pipeline.prompt import build_prompt
from app.pipeline.stub import stub_diagnosis
from app.pipeline.validator import validate_full
from app.rag.retrieve import Retriever, Retrieved


class TroubleshootError(ValueError):
    pass


async def run_troubleshoot(snapshot: dict[str, Any], retriever: Retriever) -> dict[str, Any]:
    det = snapshot.get("detector") or {}
    ftype = det.get("failureType") or "Unknown"
    question = snapshot.get("userQuestion") or ""
    hits: list[Retrieved] = retriever.search(
        list(det.get("searchTerms") or []),
        ftype,
        user_question=question,
        top_k=settings.rag_top_k,
        server_version=(snapshot.get("cluster") or {}).get("serverVersion") or "",
    )
    evidence = format_evidence(snapshot)
    mode = "mock_llm" if settings.use_mock_llm else "full_rag"
    prompt = build_prompt(evidence, hits, ftype, question)
    llm_obj = await complete_json(prompt)
    if llm_obj is None:
        diagnosis = stub_diagnosis(snapshot, hits, mode)
    else:
        llm_obj["failureType"] = ftype
        llm_obj["schemaVersion"] = "1.1.0"
        llm_obj.setdefault("mode", "full_rag")
        llm_obj["documentationReferences"] = [
            r
            for r in (llm_obj.get("documentationReferences") or [])
            if r.get("url") in {h.url for h in hits}
        ] or [
            {"title": h.title, "url": h.url, "section": h.section, "snippet": h.snippet[:240]}
            for h in hits[:4]
        ]
        self_score = llm_obj.get("confidenceScore")
        llm_obj["confidenceScore"] = hybrid_confidence(
            snapshot, hits, float(self_score) if isinstance(self_score, (int, float)) else 0.7
        )
        diagnosis = llm_obj
        mode = "full_rag"
        diagnosis["mode"] = mode

    errs = validate_full(diagnosis, snapshot, hits)
    if errs:
        if llm_obj is not None:
            diagnosis = stub_diagnosis(snapshot, hits, "mock_llm")
            errs = validate_full(diagnosis, snapshot, hits)
        if errs:
            raise TroubleshootError("; ".join(errs[:8]))
    return diagnosis
