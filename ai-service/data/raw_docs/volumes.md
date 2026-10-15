---
title: Volumes
url: https://kubernetes.io/docs/concepts/storage/volumes/
section: mount-failures
k8s_version: 1.31
doc_type: concept
failure_tags: [VolumeMountFailure]
---

FailedMount and FailedAttachVolume events mean a volume (PVC, ConfigMap, or Secret volume) could not attach or mount. The pod often remains Pending with no container start. Check PVC binding, storage class, and that volume names exist.

This is not FailedScheduling even though the pod is Pending; mount events are the primary signal while still Pending.
