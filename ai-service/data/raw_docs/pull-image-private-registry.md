---
title: Pull an Image from a Private Registry
url: https://kubernetes.io/docs/tasks/configure-pod-container/pull-image-private-registry/
k8s_version: 1.31
doc_type: task
failure_tags: [ImagePullBackOff]
---

## Creating a registry credential secret
tags: [ImagePullBackOff]

Registry credentials are stored in a Secret of type kubernetes.io/dockerconfigjson, created with kubectl create secret docker-registry or from an existing Docker config file. The Secret must live in the same namespace as the pods that use it.

## Referencing the secret
tags: [ImagePullBackOff]

Pods reference the credential through spec.imagePullSecrets. To avoid repeating it in every manifest, add it to the namespace's service account so every pod using that account inherits it. After fixing credentials, the kubelet picks them up on its next retry; deleting the pod forces an immediate retry.
