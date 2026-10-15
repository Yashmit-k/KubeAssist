---
title: Init Containers
url: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/
k8s_version: 1.31
doc_type: concept
failure_tags: [InitContainerFailure, CrashLoopBackOff, ImagePullBackOff]
---

## Init containers block the pod
tags: [InitContainerFailure]

Init containers run one at a time, in order, and each must exit successfully before the next starts; application containers start only after all of them succeed. While an init container is failing, the pod shows a status like Init:Error or Init:CrashLoopBackOff and the app containers wait in PodInitializing.

## Common causes
tags: [InitContainerFailure]

Init containers often wait for a dependency, run a migration, or fetch configuration. They fail when the dependency's hostname does not resolve or never becomes reachable, when the script exits non-zero, or when their image cannot be pulled. An init container that loops until a service answers will block forever if that service does not exist.

## Debugging
tags: [InitContainerFailure]

Read the failing init container's own logs with kubectl logs POD -c INIT_CONTAINER_NAME, adding --previous after it has restarted. kubectl describe pod lists init container states and exit codes separately from app containers.
