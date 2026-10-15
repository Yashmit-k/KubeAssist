---
title: Assign Memory Resources to Containers and Pods
url: https://kubernetes.io/docs/tasks/configure-pod-container/assign-memory-resource/
section: memory-limits
k8s_version: 1.31
doc_type: task
failure_tags: [OOMKilled, Evicted]
---

If a container exceeds its memory limit, the runtime OOMKills it (often exit 137, lastState reason OOMKilled). The pod may currently show CrashLoopBackOff while waiting; the termination reason is still OOMKilled. Raise the limit or reduce usage.

Eviction is different: the kubelet evicts pods under node MemoryPressure or DiskPressure regardless of that container's own limit. Pod status reason Evicted is node-level, not a container memory limit hit.
