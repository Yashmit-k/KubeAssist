---
title: Pod Lifecycle
url: https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/
k8s_version: 1.31
doc_type: concept
failure_tags: [CrashLoopBackOff, OOMKilled, Unknown, Healthy]
---

## Pod phases
tags: [Unknown, Healthy]

A pod's phase is a coarse summary: Pending (accepted but not all containers are running, which includes scheduling and image pulls), Running (bound to a node with at least one container running), Succeeded, Failed, and Unknown (the node stopped reporting). Phase alone rarely explains a failure; container states and events carry the detail.

## Container states and exit codes
tags: [CrashLoopBackOff, OOMKilled]

Each container is Waiting, Running or Terminated. A Terminated state records an exit code. Exit code 1 or another small number is an application error. 126 means the command was found but not executable and 127 means the command was not found, which usually points to a wrong command or entrypoint in the spec. 137 means the process was killed with SIGKILL, most often by the out-of-memory killer, and 143 means it exited on SIGTERM, for example during shutdown or after a failed liveness probe.

## Restart policy and back-off
tags: [CrashLoopBackOff]

With restartPolicy Always (the default for Deployments) the kubelet restarts exited containers with an exponential back-off delay capped at five minutes. The delay resets after a container runs successfully for ten minutes. This back-off is what the CrashLoopBackOff waiting reason reports.

## Pod conditions
tags: [Unknown, ReadinessFailure]

Pod conditions such as PodScheduled, Initialized, ContainersReady and Ready show how far a pod has progressed. A pod with PodScheduled false has not been placed on a node; a pod with Initialized false is still running init containers; ContainersReady false with all containers running usually means a readiness probe is failing.
