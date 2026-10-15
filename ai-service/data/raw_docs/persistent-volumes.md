---
title: Persistent Volumes
url: https://kubernetes.io/docs/concepts/storage/persistent-volumes/
k8s_version: 1.31
doc_type: concept
failure_tags: [VolumeMountFailure, FailedScheduling]
---

## Unbound claims
tags: [VolumeMountFailure, FailedScheduling]

A pod that uses a PersistentVolumeClaim cannot start until the claim is Bound to a volume. kubectl get pvc shows Pending claims; a claim can stay Pending because no StorageClass is set or the named StorageClass does not exist, because no provisioner is installed, or because no existing volume matches its size and access mode.

## Access modes and attachment
tags: [VolumeMountFailure]

ReadWriteOnce volumes can be attached to one node at a time. A replacement pod scheduled to a different node while the old pod still holds the volume gets Multi-Attach errors until the old attachment is released.
