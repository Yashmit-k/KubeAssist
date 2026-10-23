from __future__ import annotations

from typing import Any

LOG_CHARS = 3000


def _state(st: dict[str, Any] | None) -> str:
    if not st:
        return "-"
    parts = [str(st.get("phase") or "?")]
    if st.get("reason"):
        parts.append(f"reason={st['reason']}")
    if st.get("exitCode") is not None:
        parts.append(f"exitCode={st['exitCode']}")
    if st.get("message"):
        parts.append(f"message={str(st['message'])[:200]!r}")
    return " ".join(parts)


def _container_line(c: dict[str, Any]) -> str:
    res = c.get("resources") or {}
    probes = c.get("probes") or {}
    line = (
        f"  - {c.get('name')} image={c.get('image')} ready={c.get('ready')} restarts={c.get('restartCount')}\n"
        f"    state: {_state(c.get('state'))}\n"
        f"    lastState: {_state(c.get('lastState'))}"
    )
    if res.get("requests") or res.get("limits"):
        line += f"\n    resources: requests={res.get('requests') or {}} limits={res.get('limits') or {}}"
    for kind in ("liveness", "readiness", "startup"):
        if probes.get(kind):
            line += f"\n    {kind}Probe: {probes[kind]}"
    return line


def format_evidence(snapshot: dict[str, Any]) -> str:
    """Render the snapshot as compact, labelled text for the LLM prompt.
    Everything here comes from Go's (already redacted) snapshot."""
    pod = snapshot.get("pod") or {}
    det = snapshot.get("detector") or {}
    logs = snapshot.get("logs") or {}
    lines = [
        f"POD {pod.get('namespace')}/{pod.get('name')} phase={pod.get('phase')} node={pod.get('nodeName') or '(unscheduled)'}"
        + (f" reason={pod.get('reason')}" if pod.get("reason") else "")
        + (f" message={pod.get('message')!r}" if pod.get("message") else ""),
    ]
    owners = snapshot.get("owners") or {}
    if owners.get("deployment"):
        lines.append(f"OWNER deployment/{owners['deployment'].get('name')}")
    elif owners.get("replicaSet"):
        lines.append(f"OWNER replicaset/{owners['replicaSet'].get('name')}")

    if snapshot.get("initContainers"):
        lines.append("INIT CONTAINERS")
        lines.extend(_container_line(c) for c in snapshot["initContainers"])
    lines.append("CONTAINERS")
    lines.extend(_container_line(c) for c in snapshot.get("containers") or [])

    conds = [c for c in snapshot.get("conditions") or [] if c.get("status") != "True"]
    if conds:
        lines.append("CONDITIONS (not True)")
        for c in conds:
            lines.append(f"  - {c.get('type')}={c.get('status')} {c.get('reason') or ''} {c.get('message') or ''}".rstrip())

    lines.append("EVENTS (newest first)")
    events = snapshot.get("events") or []
    if not events:
        lines.append("  (none)")
    for e in events[:15]:
        count = f" x{e['count']}" if e.get("count") else ""
        lines.append(f"  - [{e.get('type')}] {e.get('reason')}{count}: {e.get('message')}")

    lines.append("LOGS")
    if logs.get("unavailableReason"):
        lines.append(f"  unavailable: {logs['unavailableReason']}")
    if logs.get("truncated"):
        lines.append("  (truncated to the most recent lines)")
    if logs.get("previous"):
        lines.append("  previous instance (tail):\n" + _indent(str(logs["previous"])[-LOG_CHARS:]))
    if logs.get("current"):
        lines.append("  current instance (tail):\n" + _indent(str(logs["current"])[-LOG_CHARS:]))
    if not logs.get("previous") and not logs.get("current") and not logs.get("unavailableReason"):
        lines.append("  (empty)")

    rel = snapshot.get("related") or {}
    if rel.get("services"):
        lines.append("RELATED SERVICES")
        for s in rel["services"]:
            lines.append(f"  - {s.get('name')} selector={s.get('selector')} readyEndpoints={s.get('endpointsReady')}")
        lines.append(f"  pod labels: {pod.get('labels') or {}}")
    if rel.get("configMapRefs"):
        lines.append("CONFIGMAP REFERENCES (key names only)")
        for cm in rel["configMapRefs"]:
            lines.append(f"  - {cm.get('name')} present={cm.get('keysPresent') or []} missing={cm.get('keysMissing') or []}")

    ro = snapshot.get("rollout") or {}
    if ro:
        lines.append(
            f"RECENT ROLLOUT {ro.get('workload')}: pod runs revision {ro.get('revision')} "
            f"(created {ro.get('rolledOutAt')}, latest={ro.get('latest')}); "
            f"previous revision {ro.get('previousRevision') or 'none'} (created {ro.get('previousCreatedAt') or '-'})"
        )
        for ch in ro.get("changes") or []:
            where = f" [{ch['container']}]" if ch.get("container") else ""
            name = f" {ch['name']}" if ch.get("name") else ""
            lines.append(f"  - {ch.get('field')}{name}{where}: {ch.get('from') or '(none)'} -> {ch.get('to') or '(none)'}")

    lines.append(f"DETECTOR failureType={det.get('failureType')} confidence={det.get('confidence')}")
    for ev in det.get("evidence") or []:
        lines.append(f"  - {ev}")
    for hint in det.get("relatedHints") or []:
        lines.append(f"  hint: {hint}")

    fix = det.get("fix") or {}
    if fix.get("facts"):
        lines.append("FIX FACTS (exact values from the evidence)")
        for k, v in sorted(fix["facts"].items()):
            lines.append(f"  {k}: {v}")
    if fix.get("steps"):
        lines.append("SUGGESTED FIX (commands computed from the evidence)")
        for i, st in enumerate(fix["steps"], start=1):
            tag = "write" if st.get("isWriteAction") else "read-only"
            lines.append(f"  {i}. [{tag}] {st.get('description')}")
            if st.get("command"):
                lines.append(f"     $ {st['command']}")
    return "\n".join(lines)


def _indent(text: str) -> str:
    return "\n".join("    " + ln for ln in text.rstrip().splitlines())
