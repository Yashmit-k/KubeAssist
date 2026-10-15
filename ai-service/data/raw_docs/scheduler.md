---
title: Kubernetes Scheduler
url: https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/
k8s_version: 1.31
doc_type: concept
failure_tags: [FailedScheduling]
---

## Reading FailedScheduling events
tags: [FailedScheduling]

A pod that no node can accept stays Pending with an empty nodeName and a FailedScheduling event. The event message summarizes, per reason, how many nodes were rejected, for example insufficient cpu, insufficient memory, untolerated taints, or node affinity mismatch. That breakdown is the key evidence; container logs do not exist yet.

## Resource-driven failures
tags: [FailedScheduling]

Insufficient cpu or memory means the pod's requests do not fit in any node's remaining allocatable capacity. Lower the requests to realistic values, scale down other workloads, or add nodes. A request larger than any single node can never be satisfied.

## Stale events
tags: [FailedScheduling, Healthy]

FailedScheduling events can linger after a pod has been scheduled. Only treat them as the current problem while the pod is still Pending without a node.
