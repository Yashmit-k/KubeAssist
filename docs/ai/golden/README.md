# Golden evaluation notes

Expected signals for each lab scenario. Use these to:

1. Manually verify Kind after `kubectl apply -f k8s/lab/`
2. Drive Go detector unit tests later (export real snapshots into `snapshots/` when available)
3. Drive RAG retrieval smoke queries (`searchTerms`) and AI fixture tests without waiting for Go

## Status

| Field | Meaning |
| --- | --- |
| `snapshotFixtureStatus: expected_notes_only` | Expected signals documented; live Kind export not committed yet |
| `snapshots/` (future) | Redacted `DiagnosticSnapshot` JSON matching `schemas/diagnostic-snapshot.schema.json` |

## Verification checklist

```powershell
kubectl config use-context kind-kubeassist-dev
kubectl create namespace kubeassist-lab --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f .\k8s\lab\
kubectl apply -f .\k8s\rbac\
kubectl get pods -n kubeassist-lab -o wide
```

For each file in this directory, confirm `expectedFailureType` and `expectedSignals` against `describe` / events / logs. Respect `waitHintSeconds` before judging flaky cases (OOM, ImagePull, probes).

## Detector priority reminder

`ProbeFailure` wins over `CrashLoopBackOff` when Unhealthy/Killing + probe config are present.  
`OOMKilled` wins over plain CrashLoop when `lastState.terminated.reason` is OOMKilled.  
`ServiceSelectorMismatch` requires Service/Endpoints evidence even if the pod is Healthy.
