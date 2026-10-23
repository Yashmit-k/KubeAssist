"""Rule-based planner used when no LLM is configured: maps a question to
one read-only tool call by intent keywords. Deliberately simple and
predictable — it handles the common questions well and says what it
can do otherwise."""

from __future__ import annotations

import re
from typing import Any

_NAME = re.compile(r"^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$")
_WORD = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
# Explicit "namespace X" wins over the looser "in X" form.
_NS_EXPLICIT = re.compile(r"\b(?:namespace|ns)\s+([a-z0-9][-a-z0-9]*)")
_NS_IN = re.compile(r"\bin\s+(?:the\s+)?([a-z0-9][-a-z0-9]*)")

_STOP = {
    "the", "my", "this", "that", "all", "any", "cluster", "your", "our", "a", "an", "it", "its", "there", "here",
    "pod", "pods", "logs", "log", "what", "why", "is", "are", "show", "me", "please", "kubernetes", "k8s", "now",
    "wrong", "broken", "failing", "crashing", "namespace", "namespaces", "previous", "crash", "health", "scan",
}
_PROBLEM = re.compile(r"\b(broken|failing|fail|failed|wrong|problems?|issues?|errors?|unhealthy|crash\w*|down|health|scan|fix|why|diagnos\w*|troubleshoot\w*)\b", re.I)
_LOGS = re.compile(r"\blogs?\b", re.I)
_LIST_PODS = re.compile(r"\b(list|show|which|what)\b.*\bpods?\b|\bpods?\b.*\b(running|are there)\b", re.I)
_LIST_NS = re.compile(r"\bnamespaces\b", re.I)

HELP = (
    "I can look into this cluster for you (read-only). Try:\n"
    "- \"What's broken?\" or \"What's wrong in kubeassist-lab?\" — a health scan with one fix per root cause\n"
    "- \"Why is oom-failure crashing?\" — diagnose one pod\n"
    "- \"Show logs for crashloop-app\" — recent log lines\n"
    "- \"List pods in kubeassist-lab\" or \"Which namespaces are there?\""
)


def _namespace(text: str) -> str | None:
    lower = text.lower()
    for pattern in (_NS_EXPLICIT, _NS_IN):
        for m in pattern.finditer(lower):
            if m.group(1) not in _STOP:
                return m.group(1)
    return None


def _pod_token(text: str, namespace: str | None) -> str | None:
    """The most pod-like word: a DNS-style name with a dash, not a stop
    word or the namespace. Longest wins (more specific)."""
    cands = []
    for w in _WORD.findall(text):
        w = w.strip(".").lower()
        if w in _STOP or w == namespace or "-" not in w or len(w) < 3 or not _NAME.match(w):
            continue
        cands.append(w)
    return max(cands, key=len) if cands else None


def plan(question: str) -> dict[str, Any] | None:
    """One tool call for the question, or None (answer with HELP)."""
    ns = _namespace(question)
    pod = _pod_token(question, ns)
    args: dict[str, Any] = {"namespace": ns} if ns else {}
    if _LOGS.search(question) and pod:
        call = {"name": "get_logs", "arguments": {**args, "name": pod}}
        if re.search(r"\b(previous|last crash|before (it )?crash\w*|crashed)\b", question, re.I):
            call["arguments"]["previous"] = True
        return call
    if pod and _PROBLEM.search(question):
        return {"name": "diagnose_pod", "arguments": {**args, "name": pod}}
    if _LIST_NS.search(question) and not pod:
        return {"name": "list_namespaces", "arguments": {}}
    if _LIST_PODS.search(question):
        return {"name": "list_pods", "arguments": args}
    if _PROBLEM.search(question):
        return {"name": "scan", "arguments": args}
    if pod:
        return {"name": "diagnose_pod", "arguments": {**args, "name": pod}}
    return None
