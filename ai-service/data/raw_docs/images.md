---
title: Images
url: https://kubernetes.io/docs/concepts/containers/images/
k8s_version: 1.31
doc_type: concept
failure_tags: [ImagePullBackOff, InitContainerFailure]
---

## ErrImagePull and ImagePullBackOff
tags: [ImagePullBackOff]

ErrImagePull is the immediate failure to fetch an image; ImagePullBackOff is the waiting state the kubelet enters while backing off between retries. Both mean the container has never started, so there are no application logs. The Failed event on the pod carries the registry's actual error message, which distinguishes the causes below.

## Wrong name or tag
tags: [ImagePullBackOff]

Messages such as manifest unknown, not found, or repository does not exist mean the image reference is wrong: a typo in the repository, a tag that was never pushed, or a registry host that is missing so the name resolves to Docker Hub. Check the exact image string in the pod spec against the registry.

## Authentication and private registries
tags: [ImagePullBackOff]

Messages such as unauthorized, authentication required, or access denied mean the kubelet could not authenticate. Private registries need an imagePullSecrets entry on the pod or its service account that references a kubernetes.io/dockerconfigjson Secret in the same namespace. The secret itself never needs to be shared with a troubleshooting tool; its name and presence are enough.

## Network and rate limits
tags: [ImagePullBackOff]

Timeouts, i/o errors or too many requests point at node networking, a proxy, or registry rate limits rather than the image reference. These are often transient but recur for every pod scheduled on the affected nodes.

## Pull policy
tags: [ImagePullBackOff]

imagePullPolicy controls when the kubelet pulls. Always pulls on every start; IfNotPresent uses a cached image when available; Never requires the image to already be on the node, which fails on fresh nodes. Images tagged latest default to Always.
