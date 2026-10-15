from __future__ import annotations

from typing import Any

from app.pipeline.confidence import hybrid_confidence
from app.rag.retrieve import Retrieved

_NS = "kubeassist-lab"

PLAYBOOK: dict[str, dict[str, Any]] = {
    "CrashLoopBackOff": {
        "issueSummary": "The container is crash-looping: Kubernetes is restarting it after non-zero exits.",
        "probableRootCause": "The application process exits instead of staying running.",
        "reasoning": "Go classified CrashLoopBackOff from waiting or terminated Error with restarts. Remediation follows snapshot evidence and retrieved pod-debug docs; this service does not reclassify.",
        "steps": [
            ("Read previous logs for the exit message.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", False),
            ("Fix the application or command so the process does not exit, then roll out the change.", "kubectl edit {owner} -n {ns}", True),
        ],
        "verify": ["kubectl get pod {pod} -n {ns}", "kubectl logs {pod} -n {ns} --previous --tail=50"],
        "rollback": "Revert the command or config change if the new revision is worse.",
    },
    "OOMKilled": {
        "issueSummary": "The container was OOMKilled after exceeding its memory limit.",
        "probableRootCause": "Working set exceeded the container memory limit.",
        "reasoning": "Go set OOMKilled from lastState or current termination reason. Current wait may still be CrashLoopBackOff; the type is not changed here.",
        "steps": [
            ("Confirm lastState reason OOMKilled and the memory limit.", "kubectl describe pod {pod} -n {ns}", False),
            ("Raise the memory limit or reduce application memory use.", "kubectl set resources {owner} -n {ns} -c {container} --limits=memory=<new-limit>", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}", "kubectl logs {pod} -n {ns} --previous --tail=50"],
        "rollback": "Restore the previous memory limit if the increase is not wanted.",
    },
    "ImagePullBackOff": {
        "issueSummary": "The kubelet cannot pull the container image, so the pod never starts.",
        "probableRootCause": "Image name, registry, or pull credentials are wrong.",
        "reasoning": "Go classified ImagePullBackOff (ErrImagePull is evidence only). Logs are expected to be unavailable until the image pulls.",
        "steps": [
            ("Read the pull error in the pod events.", "kubectl describe pod {pod} -n {ns}", False),
            ("Correct the image reference or add imagePullSecrets.", "kubectl set image {owner} -n {ns} {container}=<registry/image:tag>", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert the image field to the last known good tag.",
    },
    "ProbeFailure": {
        "issueSummary": "A liveness probe is failing and Kubernetes is restarting the container.",
        "probableRootCause": "The probe is too aggressive for startup time, or the app is not serving the probe path.",
        "reasoning": "Go preferred ProbeFailure over CrashLoopBackOff because Unhealthy liveness events and a liveness probe are present.",
        "steps": [
            ("Compare probe initialDelaySeconds with actual startup time from logs.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", False),
            ("Add a startupProbe or increase the liveness delay.", "kubectl edit {owner} -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}", "kubectl get events -n {ns} --field-selector reason=Unhealthy"],
        "rollback": "Restore previous probe timings if the change regresses.",
    },
    "CreateContainerConfigError": {
        "issueSummary": "The container cannot be created because referenced config is incomplete.",
        "probableRootCause": "A ConfigMap or Secret key named in the pod spec is missing.",
        "reasoning": "Go classified CreateContainerConfigError from the waiting reason. Related configMapRefs, if present, list missing keys without secret values.",
        "steps": [
            ("Read the waiting message naming the missing reference.", "kubectl describe pod {pod} -n {ns}", False),
            ("Add the missing key or fix the env reference.", "kubectl edit configmap <name> -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Remove the added key if it was incorrect.",
    },
    "FailedScheduling": {
        "issueSummary": "The scheduler cannot place the pod on any node.",
        "probableRootCause": "Resource requests, taints, or affinity cannot be satisfied.",
        "reasoning": "Go classified FailedScheduling from Pending-phase events. Logs are typically unavailable.",
        "steps": [
            ("Read FailedScheduling event messages and resource requests.", "kubectl describe pod {pod} -n {ns}", False),
            ("Lower requests or adjust scheduling constraints.", "kubectl edit {owner} -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Restore previous requests if the workload then underscales.",
    },
    "ServiceSelectorMismatch": {
        "issueSummary": "The pod may be healthy, but a Service has no ready endpoints because the selector does not match.",
        "probableRootCause": "Service selector labels do not match the pod labels.",
        "reasoning": "Go set ServiceSelectorMismatch after pass-2 related services. This service echoes that type.",
        "steps": [
            ("Compare the Service selector to the pod labels.", "kubectl get pod {pod} -n {ns} --show-labels", False),
            ("Align the Service selector with the pod labels.", "kubectl edit service <service-name> -n {ns}", True),
        ],
        "verify": ["kubectl get endpoints -n {ns}"],
        "rollback": "Revert the Service selector if it broke other workloads.",
    },
    "Healthy": {
        "issueSummary": "The collector found no failure signature; containers are ready.",
        "probableRootCause": "No pod-level failure; if traffic fails, inspect Services separately.",
        "reasoning": "Go classified Healthy. RAG explains the snapshot; it does not invent a failure type.",
        "steps": [
            ("Confirm Ready containers and Running phase.", "kubectl get pod {pod} -n {ns} -o wide", False),
        ],
        "verify": ["kubectl get pod {pod} -n {ns}"],
        "rollback": "No change was recommended.",
    },
    "Unknown": {
        "issueSummary": "No detector signature matched this snapshot.",
        "probableRootCause": "Insufficient or mixed evidence; do not guess a specific failure type.",
        "reasoning": "Go set Unknown. This service echoes Unknown and states uncertainty instead of reclassifying.",
        "steps": [
            ("Re-read events, container state, and logs.", "kubectl describe pod {pod} -n {ns}", False),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "No change was recommended.",
    },
    "Evicted": {
        "issueSummary": "The kubelet evicted the pod because of node-level resource pressure.",
        "probableRootCause": "Node MemoryPressure, DiskPressure, or similar — not the container's own OOM limit.",
        "reasoning": "Go classified Evicted from pod.reason. This is distinct from OOMKilled.",
        "steps": [
            ("Read the eviction message and check node pressure.", "kubectl describe pod {pod} -n {ns}", False),
            ("Free node resources or set requests so the pod is less likely to be evicted, then remove the failed pod.", "kubectl delete pod {pod} -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Undo node or priority changes if they were experimental.",
    },
    "InitContainerFailure": {
        "issueSummary": "An init container is failing, so application containers have not started.",
        "probableRootCause": "Init process exit or init image pull failure.",
        "reasoning": "Go classified InitContainerFailure from initContainers state. Echoed as-is.",
        "steps": [
            ("Read the failing init container's logs.", "kubectl logs {pod} -n {ns} -c {container} --previous --tail=100", False),
            ("Fix the init command, image, or the dependency it waits for.", "kubectl edit {owner} -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert the init container spec if the new one fails worse.",
    },
    "VolumeMountFailure": {
        "issueSummary": "The pod cannot start because a volume failed to attach or mount.",
        "probableRootCause": "PVC, ConfigMap, or Secret volume is missing or not bound.",
        "reasoning": "Go classified VolumeMountFailure from FailedMount/FailedAttachVolume while Pending.",
        "steps": [
            ("Read FailedMount events.", "kubectl describe pod {pod} -n {ns}", False),
            ("Fix PVC binding or volume names.", "kubectl get pvc -n {ns}", False),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Restore the previous volume spec if needed.",
    },
    "ReadinessFailure": {
        "issueSummary": "The container is running without restarts but is not Ready because readiness probes fail.",
        "probableRootCause": "The app is not passing readiness (often a dependency).",
        "reasoning": "Go classified ReadinessFailure from Unhealthy readiness events plus running/not-ready/0 restarts.",
        "steps": [
            ("Read Unhealthy readiness events.", "kubectl describe pod {pod} -n {ns}", False),
            ("Fix the dependency or the readiness probe.", "kubectl edit {owner} -n {ns}", True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert probe or dependency changes if they worsen Ready.",
    },
}


def fix_plan(snapshot: dict[str, Any]) -> dict[str, Any]:
    """The evidence-derived fix Go computed (detector.fix), or {}."""
    return (snapshot.get("detector") or {}).get("fix") or {}


def fix_write_steps(fix: dict[str, Any]) -> list[dict[str, Any]]:
    return [s for s in fix.get("steps") or [] if s.get("isWriteAction")]


def merge_fix_steps(template: list[dict[str, Any]], fix: dict[str, Any]) -> list[dict[str, Any]]:
    """Read-only template steps first, then Go's concrete fix steps.
    When the fix has a concrete write step it replaces the template's
    generic write steps (the "<new-limit>" kind); otherwise those stay.
    Orders are renumbered and duplicate commands dropped."""
    replace_writes = bool(fix_write_steps(fix))
    out: list[dict[str, Any]] = []
    seen: set[str] = set()
    candidates = [s for s in template if not (s.get("isWriteAction") and replace_writes)]
    candidates += [
        {"description": s["description"], "command": s.get("command") or None, "isWriteAction": bool(s.get("isWriteAction"))}
        for s in fix.get("steps") or []
    ]
    for s in candidates:
        cmd = s.get("command")
        if cmd and cmd in seen:
            continue
        if cmd:
            seen.add(cmd)
        out.append({**s, "order": len(out) + 1})
    return out


def _owner(snapshot: dict[str, Any]) -> str:
    owners = snapshot.get("owners") or {}
    if owners.get("deployment"):
        return f"deployment/{owners['deployment'].get('name')}"
    refs = (snapshot.get("pod") or {}).get("ownerRefs") or []
    if refs:
        return f"{str(refs[0].get('kind', 'pod')).lower()}/{refs[0].get('name')}"
    return f"pod/{(snapshot.get('pod') or {}).get('name')}"


def _focus_container(snapshot: dict[str, Any], ftype: str) -> str:
    if ftype == "InitContainerFailure":
        for c in snapshot.get("initContainers") or []:
            st = c.get("state") or {}
            if st.get("phase") != "terminated" or st.get("exitCode") not in (0, None):
                return str(c.get("name"))
    worst = sorted(
        (c for c in snapshot.get("containers") or [] if not c.get("ready")),
        key=lambda c: c.get("restartCount", 0),
        reverse=True,
    )
    if worst:
        return str(worst[0].get("name"))
    cs = snapshot.get("containers") or []
    return str(cs[0].get("name")) if cs else "<container>"


def stub_diagnosis(
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
    mode: str,
    extra_notes: list[str] | None = None,
) -> dict[str, Any]:
    pod = (snapshot.get("pod") or {}).get("name") or "POD"
    ns = (snapshot.get("pod") or {}).get("namespace") or _NS
    ftype = (snapshot.get("detector") or {}).get("failureType") or "Unknown"
    book = PLAYBOOK.get(ftype, PLAYBOOK["Unknown"])
    evidence = list((snapshot.get("detector") or {}).get("evidence") or [])
    if not evidence:
        evidence = [f"Pod phase {(snapshot.get('pod') or {}).get('phase')} with detector type {ftype}"]
    logs = snapshot.get("logs") or {}
    notes: list[str] = list(extra_notes or [])
    if mode == "mock_llm" and not extra_notes:
        notes.append("Playbook answer (no LLM configured): evidence and classification come from the detector; steps are a curated template for this failure type.")
    if logs.get("unavailableReason"):
        notes.append(f"Logs unavailable: {logs['unavailableReason']}")
    if any((c.get("restartCount") or 0) > 0 for c in snapshot.get("containers") or []) and not logs.get("previous"):
        notes.append("Previous logs were not included on the snapshot.")
    fix = fix_plan(snapshot)
    facts = fix.get("facts") or {}
    fill = {
        "pod": pod,
        "ns": ns,
        "owner": facts.get("owner") or _owner(snapshot),
        "container": facts.get("container") or _focus_container(snapshot, ftype),
    }
    steps = merge_fix_steps(
        [{"description": d, "command": c.format(**fill) if c else None, "isWriteAction": w} for d, c, w in book["steps"]],
        fix,
    )
    if fix_write_steps(fix):
        notes.append(
            "The fix commands were built from this pod's evidence. Any remaining <...> value is something only you "
            "know (for example the intended image tag or a config value) — replace it before running the command."
        )
    refs = []
    for d in retrieved[:4]:
        refs.append(
            {
                "title": d.title,
                "url": d.url,
                "section": d.section,
                "snippet": d.snippet[:240],
            }
        )
    conf = hybrid_confidence(snapshot, retrieved, llm_self=0.65 if mode == "mock_llm" else None)
    return {
        "schemaVersion": "1.1.0",
        "failureType": ftype,
        "issueSummary": book["issueSummary"],
        "observedEvidence": evidence,
        "probableRootCause": book["probableRootCause"],
        "reasoning": book["reasoning"],
        "confidenceScore": conf,
        "resolutionSteps": steps,
        "verificationCommands": [c.format(**fill) for c in book["verify"]],
        "rollbackGuidance": book["rollback"],
        "documentationReferences": refs,
        "uncertaintyNotes": notes,
        "mode": mode,
    }
