---
title: Configure a Pod to Use a ConfigMap
url: https://kubernetes.io/docs/tasks/configure-pod-container/configure-pod-configmap/
section: missing-keys
k8s_version: 1.31
doc_type: task
failure_tags: [CreateContainerConfigError]
---

CreateContainerConfigError occurs when the kubelet cannot materialize env or volume config, often because a ConfigMap or Secret key referenced by configMapKeyRef does not exist. The ConfigMap itself may exist with other keys. Do not send Secret values to an AI service; names and key presence are enough.

Fix by adding the missing key or correcting the env reference. Init containers can fail the same way before app containers start.
