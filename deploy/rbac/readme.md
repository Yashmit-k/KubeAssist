# Read-only RBAC for KubeAssist collector

Apply when the Go backend runs in-cluster (or to document least-privilege for a dedicated kubeconfig user).

```powershell
kubectl apply -f .\k8s\rbac\
```

**Rules:** `get` / `list` / `watch` only. No `create` / `update` / `patch` / `delete` / `exec`.

**Secrets:** not granted. Missing-Secret cases are diagnosed from Events (`CreateContainerConfigError`) without reading Secret values.

**ConfigMaps:** `get`/`list`/`watch` allowed for name/key existence checks. Values sent to the AI path must still be redacted or omitted by the collector.
