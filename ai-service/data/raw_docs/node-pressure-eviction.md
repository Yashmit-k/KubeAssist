---
title: Node-pressure Eviction
url: https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/
k8s_version: 1.31
doc_type: concept
failure_tags: [Evicted]
---

## What eviction is
tags: [Evicted]

When a node runs low on memory, disk space or process IDs, the kubelet proactively terminates pods to reclaim resources. The evicted pod's phase becomes Failed with status reason Evicted and a message naming the resource, for example that the node was low on ephemeral-storage or memory.

## Eviction versus OOMKilled
tags: [Evicted, OOMKilled]

Eviction is a node-level decision and can hit a pod that is well within its own limits. OOMKilled is a container-level event caused by that container exceeding its own memory limit. Look at pod.status.reason for Evicted and at the container termination reason for OOMKilled.

## Responding to eviction
tags: [Evicted]

Pods owned by a Deployment or other controller are replaced automatically, but the evicted pod objects remain until deleted. Check node conditions (MemoryPressure, DiskPressure, PIDPressure), free disk space or add capacity, set ephemeral-storage requests and limits for pods that write lots of local data, and set accurate memory requests.
