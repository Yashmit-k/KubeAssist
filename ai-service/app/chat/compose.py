"""Turns a tool result (shapes defined by the Go backend) into a chat
answer, for when no LLM is configured. The output uses the small
markdown subset the UI renders: paragraphs, "- " bullets, **bold**,
`code`, ```bash command blocks and ```log blocks."""

from __future__ import annotations

from typing import Any

_LABEL = {
    "CrashLoopBackOff": "crash loop", "OOMKilled": "out of memory", "ImagePullBackOff": "image can't be pulled",
    "CreateContainerConfigError": "missing configuration", "VolumeMountFailure": "volume can't be mounted",
    "FailedScheduling": "can't be scheduled", "ProbeFailure": "liveness probe failing",
    "ReadinessFailure": "readiness probe failing", "InitContainerFailure": "init container failing",
    "Evicted": "evicted", "ServiceSelectorMismatch": "Service selects no pods", "Healthy": "healthy", "Unknown": "unclear",
}
MAX_GROUPS = 5


def _steps(fix: dict[str, Any] | None, writes_only: bool = False) -> list[str]:
    out: list[str] = []
    for st in (fix or {}).get("steps") or []:
        if writes_only and not st.get("isWriteAction"):
            continue
        out.append(st.get("description", ""))
        if st.get("command"):
            out.append("```bash\n" + st["command"] + "\n```")
    return out


def _scan(r: dict[str, Any]) -> str:
    where = f"namespace **{r['namespace']}**" if r.get("namespace") else "all namespaces"
    groups = r.get("groups") or []
    if not groups:
        return f"I scanned {r.get('totalPods', 0)} pods in {where} — no problems found. Every pod is running and ready."
    broken = sum(len(g.get("pods") or []) for g in groups)
    lines = [f"I scanned {where}: **{broken} unhealthy pod{'s' if broken != 1 else ''}**, "
             f"**{len(groups)} root cause{'s' if len(groups) != 1 else ''}**."]
    for i, g in enumerate(groups[:MAX_GROUPS], start=1):
        pods = g.get("pods") or []
        names = ", ".join(f"`{p['name']}`" for p in pods[:4]) + (f" and {len(pods) - 4} more" if len(pods) > 4 else "")
        lines.append(f"**{i}. {g['title']}** ({_LABEL.get(g['failureType'], g['failureType'])}; {names})")
        lines += _steps(g.get("fix"), writes_only=True) or ["No exact fix could be derived — ask me to diagnose one of these pods."]
    if len(groups) > MAX_GROUPS:
        lines.append(f"…and {len(groups) - MAX_GROUPS} more root causes — open **Health scan** to see them all.")
    lines.append("Placeholders like `<value>` are things only you know. After running a fix, ask me to check the pod again.")
    return "\n\n".join(lines)


def _diagnose(r: dict[str, Any]) -> str:
    ft = r.get("failureType", "Unknown")
    if ft == "Healthy":
        return f"**{r['namespace']}/{r['name']}** is healthy — it's running and all containers are ready."
    head = f"**{r['namespace']}/{r['name']}**: {_LABEL.get(ft, ft)} (`{ft}`)"
    lines = [head + (f": {r['rootCause']}." if r.get("rootCause") else ".")]
    ev = r.get("evidence") or []
    if ev:
        lines.append("Evidence:\n" + "\n".join(f"- {e}" for e in ev[:4]))
    steps = _steps(r.get("fix"))
    lines += (["How to fix it:"] + steps) if steps else ["I couldn't derive an exact fix from the evidence — open the pod's full AI diagnosis."]
    return "\n\n".join(lines)


def _logs(r: dict[str, Any]) -> str:
    which = "previous (crashed) instance of " if r.get("previous") else ""
    logs = (r.get("logs") or "").strip()
    if not logs:
        return f"No log lines from the {which}container `{r.get('container') or '?'}` of **{r['namespace']}/{r['name']}**."
    return f"Last {r.get('lines', '')} lines from the {which}container `{r.get('container') or '?'}` of **{r['namespace']}/{r['name']}** (redacted):\n\n```log\n{logs}\n```"


def _pods(r: dict[str, Any]) -> str:
    pods = r.get("pods") or []
    where = f" in **{r['namespace']}**" if r.get("namespace") else ""
    if not pods:
        return f"No pods{where}."
    head = f"{r.get('total', len(pods))} pods{where}, {r.get('unhealthy', 0)} need attention:"
    rows = [f"- `{p['namespace']}/{p['name']}` — {p['status']} (ready {p['ready']}, restarts {p['restartCount']})" for p in pods[:25]]
    more = [f"…and {len(pods) - 25} more."] if len(pods) > 25 else []
    return "\n".join([head, *rows, *more])


def compose(name: str, result: dict[str, Any]) -> str:
    if result.get("error"):
        cands = result.get("candidates") or []
        hint = ("\n\nDid you mean one of: " + ", ".join(f"`{c}`" for c in cands[:8]) + "?") if cands else ""
        return f"{result['error']}{hint}"
    if name == "scan":
        return _scan(result)
    if name == "diagnose_pod":
        return _diagnose(result)
    if name == "get_logs":
        return _logs(result)
    if name == "list_pods":
        return _pods(result)
    if name == "list_namespaces":
        ns = result.get("namespaces") or []
        return "Namespaces: " + ", ".join(f"`{n}`" for n in ns) if ns else "No namespaces found."
    return "Done."
