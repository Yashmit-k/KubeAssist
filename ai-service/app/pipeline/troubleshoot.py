from __future__ import annotations

import logging
from typing import Any

from app.config import settings
from app.llm.client import LLMError, complete_json
from app.pipeline.confidence import hybrid_confidence
from app.pipeline.formatter import format_evidence
from app.pipeline.prompt import SYSTEM_PROMPT, build_prompt
from app.pipeline.stub import stub_diagnosis
from app.pipeline.validator import enforce_write_flags, validate_full
from app.rag.retrieve import Retriever, Retrieved

log = logging.getLogger("kubeassist.ai.pipeline")

SCHEMA_VERSION = "1.1.0"
_ALLOWED_KEYS = {
    "schemaVersion", "failureType", "issueSummary", "observedEvidence", "probableRootCause",
    "reasoning", "confidenceScore", "resolutionSteps", "verificationCommands", "rollbackGuidance",
    "documentationReferences", "uncertaintyNotes", "mode",
}


class TroubleshootError(ValueError):
    pass


def _citations(hits: list[Retrieved], limit: int = 4) -> list[dict[str, Any]]:
    return [{"title": h.title, "url": h.url, "section": h.section, "snippet": h.snippet[:240]} for h in hits[:limit]]


def _shape_llm_output(raw: dict[str, Any], snapshot: dict[str, Any], hits: list[Retrieved]) -> dict[str, Any]:
    """Coerce model output into the schema: pin the fields the model is
    not allowed to decide, drop unknown keys (the schema forbids them),
    keep only citations that came from retrieval, and score confidence
    with the hybrid formula rather than trusting the model's number."""
    ftype = (snapshot.get("detector") or {}).get("failureType") or "Unknown"
    out = {k: v for k, v in raw.items() if k in _ALLOWED_KEYS}
    out["schemaVersion"] = SCHEMA_VERSION
    out["failureType"] = ftype
    out["mode"] = "full_rag"

    for key in ("observedEvidence", "verificationCommands", "uncertaintyNotes"):
        val = out.get(key)
        out[key] = [str(x) for x in val] if isinstance(val, list) else []
    for key in ("issueSummary", "probableRootCause", "reasoning", "rollbackGuidance"):
        out[key] = str(out.get(key) or "").strip()

    steps = []
    for i, st in enumerate(out.get("resolutionSteps") or [], start=1):
        if not isinstance(st, dict) or not st.get("description"):
            continue
        cmd = st.get("command")
        steps.append({
            "order": i,
            "description": str(st["description"]),
            "command": str(cmd) if cmd else None,
            "isWriteAction": bool(st.get("isWriteAction", False)),
        })
    out["resolutionSteps"] = steps
    enforce_write_flags(out)

    allowed = {h.url: h for h in hits}
    refs = []
    for r in out.get("documentationReferences") or []:
        if isinstance(r, dict) and r.get("url") in allowed:
            h = allowed[r["url"]]
            refs.append({"title": h.title, "url": h.url, "section": str(r.get("section") or h.section), "snippet": h.snippet[:240]})
    out["documentationReferences"] = refs or _citations(hits)

    self_score = raw.get("confidenceScore")
    out["confidenceScore"] = hybrid_confidence(
        snapshot, hits, float(self_score) if isinstance(self_score, (int, float)) else None
    )
    return out


async def run_troubleshoot(snapshot: dict[str, Any], retriever: Retriever) -> dict[str, Any]:
    """Snapshot in, schema-valid diagnosis out. Retrieval always runs;
    the LLM runs when configured. Any LLM failure (network, bad JSON,
    ungrounded content) falls back to the playbook diagnosis with an
    explicit uncertainty note — the caller always gets a valid answer."""
    det = snapshot.get("detector") or {}
    ftype = det.get("failureType") or "Unknown"
    pod = snapshot.get("pod") or {}
    question = snapshot.get("userQuestion") or ""
    hits: list[Retrieved] = retriever.search(
        list(det.get("searchTerms") or []),
        ftype,
        user_question=question,
        top_k=settings.rag_top_k,
        server_version=(snapshot.get("cluster") or {}).get("serverVersion") or "",
    )

    fallback_note: str | None = None
    if not settings.use_mock_llm:
        prompt = build_prompt(format_evidence(snapshot), hits, ftype, question, str(pod.get("name")), str(pod.get("namespace")))
        try:
            raw = await complete_json(SYSTEM_PROMPT, prompt)
            if raw is not None:
                diagnosis = _shape_llm_output(raw, snapshot, hits)
                errs = validate_full(diagnosis, snapshot, hits)
                if not errs:
                    return diagnosis
                log.warning("LLM diagnosis rejected by validator: %s", "; ".join(errs[:5]))
                fallback_note = "The language model's answer failed grounding/schema validation and was discarded: " + "; ".join(errs[:3])
        except LLMError as e:
            log.warning("LLM call failed: %s", e)
            fallback_note = f"The language model could not be reached ({e}); this is the playbook answer."

    diagnosis = stub_diagnosis(snapshot, hits, "mock_llm", extra_notes=[fallback_note] if fallback_note else None)
    errs = validate_full(diagnosis, snapshot, hits)
    if errs:
        raise TroubleshootError("; ".join(errs[:8]))
    return diagnosis
