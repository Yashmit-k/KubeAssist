---
title: Debug Services
url: https://kubernetes.io/docs/tasks/debug/debug-application/debug-service/
k8s_version: 1.31
doc_type: task
failure_tags: [ServiceSelectorMismatch, ReadinessFailure, Healthy]
---

## Services select pods by labels
tags: [ServiceSelectorMismatch]

A Service routes traffic to the pods matched by its label selector. If the selector does not match the labels on the pods, for example because of a typo or a renamed app label, the Service has zero endpoints even though the pods are Running and Ready, and clients get connection refused or timeouts.

## Checking endpoints
tags: [ServiceSelectorMismatch, ReadinessFailure]

kubectl get endpointslices -l kubernetes.io/service-name=NAME shows which pod addresses back a Service. No endpoints with matching pods present means a selector mismatch; endpoints listed as not ready mean the pods match but are failing readiness. Compare kubectl get svc NAME -o yaml with kubectl get pods --show-labels.

## Ports
tags: [ServiceSelectorMismatch, Healthy]

Even with correct endpoints, traffic fails if the Service's targetPort does not match the port the container listens on. Check that targetPort names or numbers line up with the container's ports.
