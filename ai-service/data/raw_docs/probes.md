---
title: Configure Liveness, Readiness and Startup Probes
url: https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
section: probes
k8s_version: 1.31
doc_type: task
failure_tags: [ProbeFailure, ReadinessFailure, CrashLoopBackOff]
---

Liveness probes restart a container when the check fails. If initialDelaySeconds is shorter than the time the app needs to listen, kubelet Unhealthy events for liveness appear and the pod looks like CrashLoopBackOff. Prefer classifying this as ProbeFailure when Unhealthy liveness events exist together with a configured liveness probe and restarts.

Readiness probes never kill the process. A container can be Running with restartCount 0 and still not Ready if readiness fails (dependency not reachable). That is ReadinessFailure, not CrashLoopBackOff. Startup probes can delay liveness until the app has finished starting.
