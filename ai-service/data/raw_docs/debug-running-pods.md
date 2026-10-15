---
title: Debug Running Pods
url: https://kubernetes.io/docs/tasks/debug/debug-application/debug-running-pod/
k8s_version: 1.31
doc_type: task
failure_tags: [CrashLoopBackOff, Healthy, Unknown, InitContainerFailure]
---

## Reading container status
tags: [CrashLoopBackOff, Unknown]

Start with the pod's container statuses. The waiting reason (for example CrashLoopBackOff) says what the kubelet is doing now; the last termination state says why the previous instance stopped: its exit code, a reason such as Error or OOMKilled, and timestamps. restartCount shows how long the problem has been going on. kubectl describe pod shows all of this together with recent events, which is usually the fastest first look.

## Logs from the previous instance
tags: [CrashLoopBackOff, InitContainerFailure, ProbeFailure]

A crash-looping container is often between attempts when you look at it, so its current log is empty or only has startup lines. kubectl logs POD -c CONTAINER --previous returns the output of the instance that just exited, which normally contains the fatal error. Previous logs only exist once restartCount is above zero. For multi-container pods always pass -c so you read the container that is actually failing.

## CrashLoopBackOff is a symptom
tags: [CrashLoopBackOff]

CrashLoopBackOff means the container started and then exited, and the kubelet is waiting an increasing back-off delay (up to five minutes) before starting it again. The underlying cause is whatever made the process exit: a missing environment variable, a bad command or entrypoint, a dependency it gives up on, a panic. Treat it as a probe problem only if Unhealthy liveness events are present, and as a memory problem only if the last termination reason is OOMKilled.

## Interactive debugging
tags: [CrashLoopBackOff, Unknown]

If the image has no shell or the container exits too fast to exec into, kubectl debug can attach an ephemeral debug container to the running pod, or create a copy of the pod with a different command (for example sleep) so you can inspect the filesystem and environment. These commands change the cluster and should be run deliberately by an operator.

## Confirming a healthy pod
tags: [Healthy]

A pod whose phase is Running, whose containers are all Ready, and which has no recent restarts or warning events is healthy at the pod level. If users still see errors, the problem is usually elsewhere: the Service selector, endpoints, an Ingress, a NetworkPolicy, or the application's own dependencies.
