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
            ("Read previous logs for the exit message.", True, False),
            ("Fix the application or command so the process does not exit.", False, True),
        ],
        "verify": ["kubectl get pod {pod} -n {ns}", "kubectl logs {pod} -n {ns} --previous --tail=50"],
        "rollback": "Revert the command or config change if the new revision is worse.",
    },
    "OOMKilled": {
        "issueSummary": "The container was OOMKilled after exceeding its memory limit.",
        "probableRootCause": "Working set exceeded the container memory limit.",
        "reasoning": "Go set OOMKilled from lastState or current termination reason. Current wait may still be CrashLoopBackOff; the type is not changed here.",
        "steps": [
            ("Confirm lastState reason OOMKilled and the memory limit on the snapshot.", True, False),
            ("Raise the memory limit or reduce application memory use (manual apply).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}", "kubectl logs {pod} -n {ns} --previous --tail=50"],
        "rollback": "Restore the previous memory limit if the increase is not wanted.",
    },
    "ImagePullBackOff": {
        "issueSummary": "The kubelet cannot pull the container image, so the pod never starts.",
        "probableRootCause": "Image name, registry, or pull credentials are wrong.",
        "reasoning": "Go classified ImagePullBackOff (ErrImagePull is evidence only). Logs are expected to be unavailable until the image pulls.",
        "steps": [
            ("Check waiting.reason and the image field on the snapshot.", True, False),
            ("Correct the image reference or imagePullSecrets (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert the image field to the last known good tag.",
    },
    "ProbeFailure": {
        "issueSummary": "A liveness probe is failing and Kubernetes is restarting the container.",
        "probableRootCause": "The probe is too aggressive for startup time, or the app is not serving the probe path.",
        "reasoning": "Go preferred ProbeFailure over CrashLoopBackOff because Unhealthy liveness events and a liveness probe are present.",
        "steps": [
            ("Compare probe initialDelaySeconds with actual startup time from logs.", True, False),
            ("Add a startupProbe or increase liveness delay (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}", "kubectl get events -n {ns} --field-selector reason=Unhealthy"],
        "rollback": "Restore previous probe timings if the change regresses.",
    },
    "CreateContainerConfigError": {
        "issueSummary": "The container cannot be created because referenced config is incomplete.",
        "probableRootCause": "A ConfigMap or Secret key named in the pod spec is missing.",
        "reasoning": "Go classified CreateContainerConfigError from the waiting reason. Related configMapRefs, if present, list missing keys without secret values.",
        "steps": [
            ("Read the waiting message and configMapRefs keysMissing.", True, False),
            ("Add the missing key or fix the env reference (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Remove the added key if it was incorrect.",
    },
    "FailedScheduling": {
        "issueSummary": "The scheduler cannot place the pod on any node.",
        "probableRootCause": "Resource requests, taints, or affinity cannot be satisfied.",
        "reasoning": "Go classified FailedScheduling from Pending-phase events. Logs are typically unavailable.",
        "steps": [
            ("Read FailedScheduling event messages and resource requests.", True, False),
            ("Lower requests or adjust scheduling constraints (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Restore previous requests if the workload then underscales.",
    },
    "ServiceSelectorMismatch": {
        "issueSummary": "The pod may be healthy, but a Service has no ready endpoints because the selector does not match.",
        "probableRootCause": "Service selector labels do not match the pod labels.",
        "reasoning": "Go set ServiceSelectorMismatch after pass-2 related services. This service echoes that type.",
        "steps": [
            ("Compare related.services selector to pod labels and endpointsReady.", True, False),
            ("Align the Service selector with pod labels (manual).", False, True),
        ],
        "verify": ["kubectl get endpoints -n {ns}"],
        "rollback": "Revert the Service selector if it broke other workloads.",
    },
    "Healthy": {
        "issueSummary": "The collector found no failure signature; containers are ready.",
        "probableRootCause": "No pod-level failure; if traffic fails, inspect Services separately.",
        "reasoning": "Go classified Healthy. RAG explains the snapshot; it does not invent a failure type.",
        "steps": [
            ("Confirm Ready containers and Running phase on the snapshot.", True, False),
        ],
        "verify": ["kubectl get pod {pod} -n {ns}"],
        "rollback": "No change was recommended.",
    },
    "Unknown": {
        "issueSummary": "No detector signature matched this snapshot.",
        "probableRootCause": "Insufficient or mixed evidence; do not guess a specific failure type.",
        "reasoning": "Go set Unknown. This service echoes Unknown and states uncertainty instead of reclassifying.",
        "steps": [
            ("Re-read events, container state, and logs on the snapshot.", True, False),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "No change was recommended.",
    },
    "Evicted": {
        "issueSummary": "The kubelet evicted the pod because of node-level resource pressure.",
        "probableRootCause": "Node MemoryPressure, DiskPressure, or similar — not the container's own OOM limit.",
        "reasoning": "Go classified Evicted from pod.reason. This is distinct from OOMKilled.",
        "steps": [
            ("Read pod.reason and pod.message; check node pressure if you have cluster access.", True, False),
            ("Free node resources or add requests so the pod is less likely to be evicted (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Undo node or priority changes if they were experimental.",
    },
    "InitContainerFailure": {
        "issueSummary": "An init container is failing, so application containers have not started.",
        "probableRootCause": "Init process exit or init image pull failure.",
        "reasoning": "Go classified InitContainerFailure from initContainers state. Echoed as-is.",
        "steps": [
            ("Inspect init container waiting/terminated reason and previous logs.", True, False),
            ("Fix the init command, image, or config (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert the init container spec if the new one fails worse.",
    },
    "VolumeMountFailure": {
        "issueSummary": "The pod cannot start because a volume failed to attach or mount.",
        "probableRootCause": "PVC, ConfigMap, or Secret volume is missing or not bound.",
        "reasoning": "Go classified VolumeMountFailure from FailedMount/FailedAttachVolume while Pending.",
        "steps": [
            ("Read FailedMount events on the snapshot.", True, False),
            ("Fix PVC binding or volume names (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Restore the previous volume spec if needed.",
    },
    "ReadinessFailure": {
        "issueSummary": "The container is running without restarts but is not Ready because readiness probes fail.",
        "probableRootCause": "The app is not passing readiness (often a dependency).",
        "reasoning": "Go classified ReadinessFailure from Unhealthy readiness events plus running/not-ready/0 restarts.",
        "steps": [
            ("Read Unhealthy readiness events and whether the container is running.", True, False),
            ("Fix the dependency or readiness probe (manual).", False, True),
        ],
        "verify": ["kubectl describe pod {pod} -n {ns}"],
        "rollback": "Revert probe or dependency changes if they worsen Ready.",
    },
}


def stub_diagnosis(
    snapshot: dict[str, Any],
    retrieved: list[Retrieved],
    mode: str,
) -> dict[str, Any]:
    pod = (snapshot.get("pod") or {}).get("name") or "POD"
    ns = (snapshot.get("pod") or {}).get("namespace") or _NS
    ftype = (snapshot.get("detector") or {}).get("failureType") or "Unknown"
    book = PLAYBOOK.get(ftype, PLAYBOOK["Unknown"])
    evidence = list((snapshot.get("detector") or {}).get("evidence") or [])
    if not evidence:
        evidence = [f"Pod phase {(snapshot.get('pod') or {}).get('phase')} with detector type {ftype}"]
    logs = snapshot.get("logs") or {}
    notes: list[str] = []
    if logs.get("unavailableReason"):
        notes.append(f"Logs unavailable: {logs['unavailableReason']}")
    if (snapshot.get("containers") or [{}])[0].get("restartCount", 0) > 0 and not logs.get("previous"):
        notes.append("Previous logs were not included on the snapshot.")
    steps = []
    for i, (desc, readonly, write) in enumerate(book["steps"], start=1):
        cmd = None
        if readonly:
            cmd = f"kubectl describe pod {pod} -n {ns}"
        steps.append(
            {
                "order": i,
                "description": desc,
                "command": cmd,
                "isWriteAction": write,
            }
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
        "verificationCommands": [c.format(pod=pod, ns=ns) for c in book["verify"]],
        "rollbackGuidance": book["rollback"],
        "documentationReferences": refs,
        "uncertaintyNotes": notes,
        "mode": mode,
    }
