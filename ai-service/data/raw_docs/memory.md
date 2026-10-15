---
title: Assign Memory Resources to Containers and Pods
url: https://kubernetes.io/docs/tasks/configure-pod-container/assign-memory-resource/
k8s_version: 1.31
doc_type: task
failure_tags: [OOMKilled, Evicted]
---

## Requests and limits
tags: [OOMKilled, FailedScheduling]

A memory request is what the scheduler reserves for the container on a node; a memory limit is the hard ceiling the container runtime enforces. A container may use more than its request when the node has spare memory, but never more than its limit.

## Exceeding the limit: OOMKilled
tags: [OOMKilled]

When a container's memory use reaches its limit the kernel's out-of-memory killer terminates it. The container status shows a termination reason of OOMKilled and usually exit code 137. Because the container is then restarted, the pod frequently shows CrashLoopBackOff as its current waiting reason while the last termination reason is OOMKilled; the memory limit is the real cause.

## Fixing OOMKilled
tags: [OOMKilled]

Either raise the memory limit (and request) to fit the application's real working set, or reduce what the application uses, for example by bounding caches, lowering concurrency, or configuring the language runtime's heap size so it stays under the container limit. JVM, Node.js and Go programs each have settings to respect cgroup limits.

## Requests larger than any node
tags: [FailedScheduling]

A memory request larger than the allocatable memory of every node means the pod can never be scheduled and stays Pending with a FailedScheduling event mentioning insufficient memory.
