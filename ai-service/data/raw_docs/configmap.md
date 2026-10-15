---
title: Configure a Pod to Use a ConfigMap
url: https://kubernetes.io/docs/tasks/configure-pod-container/configure-pod-configmap/
k8s_version: 1.31
doc_type: task
failure_tags: [CreateContainerConfigError, VolumeMountFailure]
---

## Missing ConfigMaps and keys
tags: [CreateContainerConfigError]

If a container's env uses configMapKeyRef and the ConfigMap or the specific key does not exist, the kubelet cannot build the container's environment. The container stays Waiting with reason CreateContainerConfigError and a message naming the missing ConfigMap or key. Very often the ConfigMap exists but with a differently spelled key.

## Fixing the reference
tags: [CreateContainerConfigError]

Either add the missing key to the ConfigMap or correct the name in the pod spec. Once the referenced data exists the kubelet retries automatically and the container starts; no restart of the pod is required. Marking a reference optional: true lets the container start without it.

## envFrom and volumes
tags: [CreateContainerConfigError, VolumeMountFailure]

envFrom imports every key of a ConfigMap as environment variables and fails the same way if the ConfigMap is missing. A ConfigMap mounted as a volume that does not exist produces FailedMount events instead, and the pod stays in ContainerCreating.
