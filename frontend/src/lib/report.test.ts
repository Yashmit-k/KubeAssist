import { describe, expect, it } from "vitest";
import { diagnosisReport, reportFileName, scanReport } from "./report";
import type { Diagnosis, ScanResult } from "./types";

const at = new Date("2026-10-24T12:00:00Z");

const diagnosis: Diagnosis = {
  schemaVersion: "1.1.0",
  failureType: "OOMKilled",
  issueSummary: "The container was killed for exceeding its memory limit.",
  observedEvidence: ['Container "app" was OOMKilled (memory limit: 32Mi)'],
  probableRootCause: "Memory use exceeded the limit.",
  reasoning: "",
  confidenceScore: 0.92,
  resolutionSteps: [
    { order: 2, description: "Raise the limit to 64Mi", command: "kubectl set resources deployment/oom -n lab -c app --limits=memory=64Mi", isWriteAction: true },
    { order: 1, description: "Confirm", command: "kubectl describe pod oom-1 -n lab", isWriteAction: false },
  ],
  verificationCommands: ["kubectl get pod -n lab -w"],
  rollbackGuidance: "kubectl rollout undo deployment/oom -n lab",
  documentationReferences: [{ title: "Assign Memory", url: "https://kubernetes.io/docs/x" }],
  uncertaintyNotes: [],
  mode: "mock_llm",
};

describe("diagnosisReport", () => {
  it("renders a complete, ordered incident report", () => {
    const md = diagnosisReport({
      cluster: "kind-dev", namespace: "lab", pod: "oom-1", diagnosis, generatedAt: at,
      verify: { verdict: "still_failing", message: "Still OOMKilled.", replaced: true, evidence: [], checkedAt: "2026-10-24T12:05:00Z",
        pod: { namespace: "lab", name: "oom-2", status: "CrashLoopBackOff", ready: "0/1", restartCount: 3 } },
    });
    expect(md.split("\n")[0]).toBe("# Out of memory: lab/oom-1");
    expect(md).toContain("- **Report generated:** 2026-10-24 12:00:00 UTC");
    expect(md).toContain("(confidence 92%)");
    // Steps sorted by order; write steps flagged; commands fenced.
    expect(md.indexOf("1. Confirm")).toBeLessThan(md.indexOf("2. Raise the limit to 64Mi _(changes the cluster)_"));
    expect(md).toContain("   ```bash\n   kubectl set resources deployment/oom -n lab -c app --limits=memory=64Mi\n   ```");
    expect(md).toContain("**❌ Still failing** — checked 2026-10-24 12:05:00 UTC");
    expect(md).toContain("Pod `oom-2`: CrashLoopBackOff, ready 0/1, restarts 3");
    expect(md).toContain("- [Assign Memory](https://kubernetes.io/docs/x)");
    expect(md).toContain("KubeAssist executed nothing");
    expect(md).not.toContain("## Caveats");
  });
});

describe("scanReport", () => {
  it("lists every root cause with its pods and fix", () => {
    const scan: ScanResult = {
      namespace: "lab", scannedAt: "2026-10-24T12:00:00Z", totalPods: 5, checked: 3, truncated: false, healthy: 1, errors: [],
      groups: [{
        key: "k", title: 'ConfigMap "cfg" is missing key "url"', failureType: "CreateContainerConfigError",
        pods: [{ namespace: "lab", name: "a-1", status: "CreateContainerConfigError", ready: "0/1", restartCount: 0 }, { namespace: "lab", name: "b-1", status: "CreateContainerConfigError", ready: "0/1", restartCount: 0 }],
        evidence: ["missing key"], fix: { steps: [{ description: "Add it", command: "kubectl patch configmap cfg -n lab", isWriteAction: true }] },
      }],
    };
    const md = scanReport({ cluster: "kind-dev", scan });
    expect(md).toContain("**unhealthy:** 2 · **root causes:** 1");
    expect(md).toContain('## 1. ConfigMap "cfg" is missing key "url"');
    expect(md).toContain("— 2 pods:");
    expect(md).toContain("- `lab/b-1` — CreateContainerConfigError, ready 0/1, restarts 0");
    expect(md).toContain("1. Add it _(changes the cluster)_");
  });

  it("says so when nothing is wrong", () => {
    const md = scanReport({ cluster: "c", scan: { scannedAt: "2026-10-24T12:00:00Z", totalPods: 3, checked: 0, truncated: false, healthy: 0, groups: [], errors: [] } });
    expect(md).toContain("No problems found");
  });
});

it("reportFileName is filesystem-safe", () => {
  expect(reportFileName("kubeassist-lab", "oom-failure-8699cfb77b-cq9cg")).toBe("kubeassist-kubeassist-lab-oom-failure-8699cfb77b-cq9cg.md");
  expect(reportFileName("../etc", "a b")).toBe("kubeassist-etc-a-b.md");
});
