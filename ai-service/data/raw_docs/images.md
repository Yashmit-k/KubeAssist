---
title: Images
url: https://kubernetes.io/docs/concepts/containers/images/
section: image-pull
k8s_version: 1.31
doc_type: concept
failure_tags: [ImagePullBackOff, InitContainerFailure]
---

ImagePullBackOff and ErrImagePull mean the kubelet could not fetch the image. Common causes are a wrong registry host or tag, missing imagePullSecrets for a private registry, or rate limits. ErrImagePull is the immediate failure; ImagePullBackOff is the backoff state. Detectors should emit failureType ImagePullBackOff for both.

The container has not started, so application logs are usually unavailable. Evidence is waiting.reason, the image field, and Failed events. Fix the image reference or credentials, then the pod can start.
