from __future__ import annotations

from app.rag.retrieve import Retrieved

SYSTEM_PROMPT = """You are a senior Kubernetes SRE writing a troubleshooting report.
Rules you must follow:
- The failure type has already been decided by a deterministic detector. Never change or question it.
- Use only facts present in the SNAPSHOT. Never invent log lines, events, exit codes, names or values.
- Cite only documents listed under RETRIEVED DOCS, using their exact URLs.
- Commands are suggestions a human will run; never claim anything was executed.
- Mark every command that changes cluster state (apply, edit, patch, delete, scale, set, rollout restart/undo, create, label, annotate, cordon, drain) with "isWriteAction": true.
- Prefer read-only verification commands (get, describe, logs, events, top).
- If evidence is missing (for example no logs), say so in uncertaintyNotes instead of guessing.
- Respond with a single JSON object and nothing else."""

OUTPUT_SHAPE = """{
  "issueSummary": "one or two sentences: what is wrong, in plain language",
  "observedEvidence": ["bullet quoting or paraphrasing a specific SNAPSHOT fact", "..."],
  "probableRootCause": "the most likely underlying cause, grounded in the evidence",
  "reasoning": "how the evidence leads to that cause, referencing docs by [number] where relevant",
  "confidenceScore": 0.0,
  "resolutionSteps": [
    {"order": 1, "description": "what to do and why", "command": "kubectl ... or null", "isWriteAction": false}
  ],
  "verificationCommands": ["read-only kubectl command proving the fix worked"],
  "rollbackGuidance": "how to undo the fix if it makes things worse",
  "documentationReferences": [{"title": "...", "url": "exact URL from RETRIEVED DOCS", "section": "..."}],
  "uncertaintyNotes": ["explicit gaps or assumptions"]
}"""


def build_prompt(evidence: str, docs: list[Retrieved], failure_type: str, question: str, pod: str, namespace: str) -> str:
    doc_block = "\n\n".join(
        f"[{i + 1}] {d.title} — {d.section}\nURL: {d.url}\n{d.snippet}" for i, d in enumerate(docs)
    ) or "(no retrieved docs)"
    q = question or "Why is this pod failing and how do I fix it?"
    return f"""FAILURE TYPE (fixed, from the detector): {failure_type}
POD: {pod}   NAMESPACE: {namespace}
Use these real names in every command.

USER QUESTION:
{q}

SNAPSHOT:
{evidence}

RETRIEVED DOCS:
{doc_block}

Return a JSON object with exactly these keys:
{OUTPUT_SHAPE}

confidenceScore is your own 0..1 estimate of how well the evidence supports the root cause.
Provide 2-5 resolutionSteps, starting with read-only investigation and ending with the fix.
"""
