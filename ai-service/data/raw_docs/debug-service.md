---
title: Debug Services
url: https://kubernetes.io/docs/tasks/debug/debug-application/debug-service/
section: selectors
k8s_version: 1.31
doc_type: task
failure_tags: [ServiceSelectorMismatch, ReadinessFailure, Healthy]
---

A Service selects pods by label selector. If the selector does not match the pod labels, Endpoints stay empty (endpointsReady 0) even when the pod is Healthy and Ready. Align the Service selector with the pod labels.

If the selector matches but the pod is not Ready, endpoints can also be empty; that is readiness, not a selector mismatch. Confirm selector, labels, and ready endpoint count together.
