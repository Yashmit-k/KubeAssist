---
title: Configure Liveness, Readiness and Startup Probes
url: https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
k8s_version: 1.31
doc_type: task
failure_tags: [ProbeFailure, ReadinessFailure, CrashLoopBackOff]
---

## Liveness probes restart containers
tags: [ProbeFailure]

A liveness probe tells the kubelet when to restart a container. When it fails failureThreshold times in a row the kubelet kills the container, which then restarts and can end up in CrashLoopBackOff. The tell-tale evidence is Unhealthy events mentioning a liveness probe failure, followed by Killing events, on a container that has a liveness probe configured and a rising restart count.

## Probe timing
tags: [ProbeFailure]

The most common liveness problem is timing. initialDelaySeconds shorter than the application's real startup time makes the probe fail before the app is listening. timeoutSeconds of one second is easily exceeded by a busy or garbage-collecting app. Compare the probe's delay, period, timeout and threshold with when the logs show the app becoming ready.

## Startup probes for slow starters
tags: [ProbeFailure]

A startup probe disables liveness and readiness checks until it succeeds, giving slow-starting applications time to boot without loosening the liveness probe for the rest of the container's life. Set failureThreshold multiplied by periodSeconds to cover the worst-case startup time.

## Readiness probes gate traffic
tags: [ReadinessFailure, ServiceSelectorMismatch]

A readiness probe never restarts a container. While it fails, the pod is removed from the ready endpoints of every Service that selects it, so it receives no traffic. A container that is Running with zero restarts but not Ready points at readiness: often a dependency the app checks (a database, another service) is unreachable, or the probe path or port is wrong.

## Probe targets
tags: [ProbeFailure, ReadinessFailure]

Probes can be an HTTP GET (success is a status from 200 to 399), a TCP connect, a gRPC health check, or an exec command (success is exit 0). Check that the path and port in the probe match what the application actually serves; a probe pointing at a port the app does not listen on fails forever.
