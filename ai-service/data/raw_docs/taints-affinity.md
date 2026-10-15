---
title: Taints, Tolerations and Node Affinity
url: https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/
k8s_version: 1.31
doc_type: concept
failure_tags: [FailedScheduling]
---

## Taints and tolerations
tags: [FailedScheduling]

A taint on a node repels pods that do not tolerate it. Control-plane nodes are commonly tainted so ordinary workloads avoid them. A FailedScheduling message about untolerated taints means every candidate node carries a taint the pod lacks a toleration for; add the toleration only if the pod really belongs on those nodes.

## nodeSelector and affinity
tags: [FailedScheduling]

nodeSelector and required node affinity restrict a pod to nodes with matching labels. If no node carries the label, for example because of a typo or a node pool that was removed, scheduling fails with a node affinity or selector mismatch. Check node labels with kubectl get nodes --show-labels.
