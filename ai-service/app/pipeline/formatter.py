from __future__ import annotations

from typing import Any


def format_evidence(snapshot: dict[str, Any]) -> str:
    pod = snapshot.get("pod") or {}
    det = snapshot.get("detector") or {}
    logs = snapshot.get("logs") or {}
    lines = [
        f"STATUS pod={pod.get('name')} ns={pod.get('namespace')} phase={pod.get('phase')} "
        f"reason={pod.get('reason') or ''} node={pod.get('nodeName')}",
        "CONTAINERS",
    ]
    for c in snapshot.get("containers") or []:
        st = c.get("state") or {}
        ls = c.get("lastState") or {}
        lines.append(
            f"  {c.get('name')} image={c.get('image')} ready={c.get('ready')} "
            f"restarts={c.get('restartCount')} state.phase={st.get('phase')} "
            f"state.reason={st.get('reason')} lastState.reason={ls.get('reason')} "
            f"limitsMemory={(c.get('resources') or {}).get('limitsMemory')} "
            f"liveness={(c.get('probes') or {}).get('liveness')}"
        )
    if snapshot.get("initContainers"):
        lines.append("INIT")
        for c in snapshot["initContainers"]:
            st = c.get("state") or {}
            lines.append(
                f"  {c.get('name')} restarts={c.get('restartCount')} "
                f"phase={st.get('phase')} reason={st.get('reason')}"
            )
    lines.append("EVENTS")
    for e in (snapshot.get("events") or [])[:15]:
        lines.append(f"  {e.get('reason')}: {e.get('message')}")
    lines.append("LOGS")
    lines.append(f"  truncated={logs.get('truncated')} unavailable={logs.get('unavailableReason')}")
    if logs.get("current"):
        lines.append("  current:\n" + str(logs["current"])[:2000])
    if logs.get("previous"):
        lines.append("  previous:\n" + str(logs["previous"])[:2000])
    rel = snapshot.get("related") or {}
    if rel.get("services"):
        lines.append("RELATED SERVICES")
        for s in rel["services"]:
            lines.append(
                f"  {s.get('name')} selector={s.get('selector')} endpointsReady={s.get('endpointsReady')}"
            )
    if rel.get("configMapRefs"):
        lines.append("CONFIGMAPS")
        for cm in rel["configMapRefs"]:
            lines.append(
                f"  {cm.get('name')} present={cm.get('keysPresent')} missing={cm.get('keysMissing')}"
            )
    lines.append("DETECTOR")
    lines.append(f"  failureType={det.get('failureType')} confidence={det.get('confidence')}")
    for ev in det.get("evidence") or []:
        lines.append(f"  - {ev}")
    q = snapshot.get("userQuestion") or ""
    if q:
        lines.append(f"USER_QUESTION {q}")
    return "\n".join(lines)


def snapshot_blob(snapshot: dict[str, Any]) -> str:
    return format_evidence(snapshot).lower()
