from __future__ import annotations

import re
from typing import Any

from app.rag.retrieve import Retrieved
from app.schema_util import validate_diagnosis

_WRITE_VERBS = {
    "apply", "create", "edit", "patch", "delete", "replace", "scale", "autoscale", "label",
    "annotate", "taint", "cordon", "uncordon", "drain", "set", "expose", "run", "debug", "cp", "exec",
}
_ROLLOUT_WRITE = {"restart", "undo", "pause", "resume"}
_HELM_WRITE = {"install", "upgrade", "uninstall", "rollback", "delete"}
# kubectl global flags that take a separate value ("-n ns", "--context x").
_VALUE_FLAGS = {"-n", "--namespace", "--context", "--cluster", "--user", "--kubeconfig", "-s", "--server", "--as", "--token"}


def _verbs(tokens: list[str]) -> list[str]:
    """Return the sub-command words after the binary, skipping flags."""
    out: list[str] = []
    skip = False
    for tok in tokens[1:]:
        if skip:
            skip = False
            continue
        if tok.startswith("-"):
            skip = tok in _VALUE_FLAGS
            continue
        out.append(tok.lower())
        if len(out) == 2:
            break
    return out


def is_write_command(cmd: str | None) -> bool:
    """True if any kubectl/helm invocation in cmd changes cluster state.
    Handles pipelines and chains (`a | kubectl apply -f -`, `a && b`)."""
    if not cmd:
        return False
    for segment in re.split(r"\|\||&&|[|;]", cmd):
        tokens = segment.split()
        while tokens and ("=" in tokens[0] or tokens[0] in {"sudo", "env"}):
            tokens = tokens[1:]  # VAR=x kubectl ..., sudo kubectl ...
        if not tokens:
            continue
        binary = tokens[0].rsplit("/", 1)[-1]
        verbs = _verbs(tokens)
        if not verbs:
            continue
        if binary in {"kubectl", "oc", "k"}:
            if verbs[0] in _WRITE_VERBS:
                return True
            if verbs[0] == "rollout" and len(verbs) > 1 and verbs[1] in _ROLLOUT_WRITE:
                return True
        elif binary == "helm" and verbs[0] in _HELM_WRITE:
            return True
    return False


def enforce_write_flags(diagnosis: dict[str, Any]) -> None:
    """Never trust the model to label its own commands: any step whose
    command mutates the cluster is flagged isWriteAction, whatever the
    model said. The UI renders these with a warning; nothing is ever
    executed automatically either way."""
    for step in diagnosis.get("resolutionSteps") or []:
        if is_write_command(step.get("command")):
            step["isWriteAction"] = True


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
        if url and url not in allowed:
            errs.append(f"citation url not in retrieval: {url}")
    blob = _blob(snapshot)
    for bullet in diagnosis.get("observedEvidence") or []:
        if not _loosely_grounded(bullet, blob, snapshot):
            errs.append(f"evidence not grounded in snapshot: {bullet[:80]}")
    for step in diagnosis.get("resolutionSteps") or []:
        if is_write_command(step.get("command")) and not step.get("isWriteAction"):
            errs.append(f"write command not flagged isWriteAction: {step.get('command')}")
    return errs


def validate_full(
    diagnosis: dict[str, Any],
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
) -> list[str]:
    return validate_diagnosis(diagnosis) + grounding_errors(diagnosis, snapshot, retrieved)


def _blob(snapshot: dict[str, Any]) -> str:
    return str(snapshot).lower()


_TOKEN_RE = re.compile(r"[a-z0-9][a-z0-9_.:/-]{3,}")
_SPECIFIC_RE = re.compile(r"[0-9_:/-]")
_STOP = {
    "container", "containers", "pod", "pods", "with", "that", "this", "from", "the", "and",
    "has", "have", "was", "were", "which", "shows", "show", "status", "kubernetes", "cluster",
    "state", "because", "there", "their", "into", "after", "before", "being",
}


def _loosely_grounded(bullet: str, blob: str, snapshot: dict[str, Any]) -> bool:
    """An evidence bullet must be traceable to the snapshot. Two rules:
    every SPECIFIC token (names, numbers, timestamps — anything with a
    digit, dash, colon or slash) must appear in the snapshot, since those
    are exactly what a model invents; and the bullet must share at least
    some concrete vocabulary with it. Paraphrase is fine; new facts aren't."""
    b = bullet.lower()
    det = snapshot.get("detector") or {}
    for ev in det.get("evidence") or []:
        e = ev.lower()
        if e[:40] in b or b[:40] in e:
            return True
    tokens = [t.strip(".,:;()'\"") for t in _TOKEN_RE.findall(b)]
    tokens = [t for t in tokens if t and t not in _STOP]
    specific = [t for t in tokens if _SPECIFIC_RE.search(t)]
    if any(t not in blob for t in specific):
        return False
    hits = sum(1 for t in tokens if t in blob)
    return hits >= 2 or (hits >= 1 and len(tokens) <= 3)
