---
title: Resource Management for Pods and Containers
url: https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
k8s_version: 1.31
doc_type: concept
failure_tags: [OOMKilled, FailedScheduling, Evicted]
---

## How requests affect scheduling
tags: [FailedScheduling]

The scheduler places a pod only on a node whose allocatable capacity minus the requests of pods already there can fit the new pod's CPU and memory requests. Actual usage does not matter for this decision, so oversized requests can leave a cluster looking idle while new pods stay Pending.

## CPU limits throttle, memory limits kill
tags: [OOMKilled]

Exceeding a CPU limit causes throttling, which shows up as slowness and possibly probe timeouts but never kills the container. Exceeding a memory limit causes an OOM kill. This asymmetry is why memory limits need headroom above the application's peak usage.

## Quality of service classes
tags: [Evicted]

Pods are classed as Guaranteed (requests equal limits for every container), Burstable, or BestEffort (no requests or limits). Under node pressure the kubelet evicts BestEffort pods first and Guaranteed pods last, so setting accurate requests makes a workload less likely to be evicted.
