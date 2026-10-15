---
title: Volumes
url: https://kubernetes.io/docs/concepts/storage/volumes/
k8s_version: 1.31
doc_type: concept
failure_tags: [VolumeMountFailure]
---

## FailedMount and FailedAttachVolume
tags: [VolumeMountFailure]

When a volume cannot be attached to the node or mounted into the pod, the pod stays in ContainerCreating and the kubelet emits FailedMount or FailedAttachVolume events naming the volume and the reason. No container has started, so there are no application logs.

## Missing ConfigMap or Secret volumes
tags: [VolumeMountFailure]

A volume that references a ConfigMap or Secret that does not exist fails with a message that the object was not found. Create the object or correct the name; the kubelet retries the mount automatically.
