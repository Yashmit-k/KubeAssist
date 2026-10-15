---
title: Secrets
url: https://kubernetes.io/docs/concepts/configuration/secret/
k8s_version: 1.31
doc_type: concept
failure_tags: [CreateContainerConfigError, VolumeMountFailure, ImagePullBackOff]
---

## Missing Secrets
tags: [CreateContainerConfigError, VolumeMountFailure]

References to Secrets behave like ConfigMap references: a missing Secret or key in env (secretKeyRef) gives CreateContainerConfigError, and a missing Secret volume gives FailedMount. Troubleshooting only requires knowing which Secret name and key are referenced and whether they exist, never the secret values themselves.

## Namespacing
tags: [CreateContainerConfigError, ImagePullBackOff]

Secrets are namespaced and can only be referenced by pods in the same namespace. A Secret created in default is invisible to a pod in another namespace, which is a frequent cause of missing-secret errors after moving a workload.
