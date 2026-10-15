---
title: Kubernetes Scheduler
url: https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/
section: pending
k8s_version: 1.31
doc_type: concept
failure_tags: [FailedScheduling]
---

Pods stay Pending with FailedScheduling events when no node can satisfy requests, taints, or affinity. Absurd CPU or memory requests (for example thousands of cores) never bind. nodeName is empty and container logs are unavailable.

Lower resource requests, relax node selectors, or add capacity. Historical FailedScheduling events on a Running pod should not be treated as the current failure.
