from __future__ import annotations

from typing import Any

from app.rag.retrieve import Retrieved


def build_prompt(evidence: str, docs: list[Retrieved], failure_type: str, question: str) -> str:
    doc_block = "\n\n".join(
        f"[{i + 1}] {d.title} ({d.url} #{d.section})\n{d.snippet}" for i, d in enumerate(docs)
    ) or "(no retrieved docs)"
    q = question or "Why is this pod failing and how do I fix it?"
    return f"""You are an SRE assistant. Classify nothing: failureType is already {failure_type}.
Use only SNAPSHOT evidence. Do not invent logs, events, or cluster facts.
Cite only URLs that appear in RETRIEVED DOCS.
Commands are suggestions; never claim they were executed.

USER QUESTION:
{q}

SNAPSHOT:
{evidence}

RETRIEVED DOCS:
{doc_block}

Return a JSON object matching DiagnosisResponse (schemaVersion 1.1.0) with failureType exactly {failure_type}.
"""
