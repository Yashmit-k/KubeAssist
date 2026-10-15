---
title: Debug Running Pods
url: https://kubernetes.io/docs/tasks/debug/debug-application/debug-running-pod/
section: logs-and-restarts
k8s_version: 1.31
doc_type: task
failure_tags: [CrashLoopBackOff, Healthy, InitContainerFailure, Unknown]
---

When a container repeatedly exits, Kubernetes marks it CrashLoopBackOff and increases the delay between restart attempts. Inspect the current container status (waiting reason, last termination exit code) and read logs from the previous instance with kubectl logs --previous once restartCount is greater than zero.

Do not assume a crash loop is a probe problem unless Unhealthy liveness events are present. Application configuration errors that print FATAL messages and exit non-zero are ordinary CrashLoopBackOff. Init containers that fail block regular containers from starting; debug init status separately.

A Healthy pod has phase Running and all containers Ready. Absence of waiting reasons and restart storms supports a Healthy classification from the collector.
