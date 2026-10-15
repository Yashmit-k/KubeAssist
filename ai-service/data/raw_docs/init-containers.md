---
title: Init Containers
url: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/
section: init-failures
k8s_version: 1.31
doc_type: concept
failure_tags: [InitContainerFailure, CrashLoopBackOff, ImagePullBackOff]
---

Init containers run to completion before app containers. If an init container crash-loops or cannot pull its image, the pod never becomes Ready. Inspect init container waiting or terminated state and restartCount; a snapshot may catch either half of the crash cycle.

Use previous logs for the failing init container. Image pull on init is still an image problem; crash-looping init is process exit.
