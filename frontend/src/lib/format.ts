import type { FailureType } from "./types";

export function age(iso?: string): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t) || t <= 0) return "—";
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

export function dateTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export type Tone = "ok" | "warn" | "bad" | "info" | "muted";

/** Colour for a kubectl-style pod STATUS string. */
export function statusTone(status: string): Tone {
  if (status === "Running" || status === "Completed" || status === "Succeeded") return "ok";
  const transitional = ["Pending", "ContainerCreating", "PodInitializing", "Terminating"];
  if (transitional.includes(status) || /^Init:\d+\/\d+$/.test(status)) return "warn";
  return "bad";
}

export function failureTone(ft: FailureType | string): Tone {
  if (ft === "Healthy") return "ok";
  if (ft === "Unknown") return "muted";
  if (ft === "ReadinessFailure" || ft === "ServiceSelectorMismatch" || ft === "FailedScheduling") return "warn";
  return "bad";
}

const FAILURE_LABELS: Record<string, string> = {
  CrashLoopBackOff: "Crash loop",
  OOMKilled: "Out of memory",
  ImagePullBackOff: "Image pull failure",
  InitContainerFailure: "Init container failure",
  VolumeMountFailure: "Volume mount failure",
  CreateContainerConfigError: "Missing config",
  FailedScheduling: "Cannot schedule",
  ProbeFailure: "Liveness probe failure",
  ReadinessFailure: "Readiness failure",
  Evicted: "Evicted",
  ServiceSelectorMismatch: "Service selector mismatch",
  Healthy: "Healthy",
  Unknown: "Unknown",
};

export function failureLabel(ft: string): string {
  return FAILURE_LABELS[ft] ?? ft;
}

export function modeLabel(mode?: string): { label: string; hint: string } {
  switch (mode) {
    case "full_rag":
      return { label: "AI + docs", hint: "Written by the language model from the evidence and retrieved Kubernetes docs, then validated." };
    case "mock_llm":
      return { label: "Playbook + docs", hint: "No language model configured (or it failed): curated steps for this failure type, with retrieved docs." };
    case "detector_only_stub":
      return { label: "Detector only", hint: "The AI service is not available: deterministic classification and a generic playbook." };
    default:
      return { label: mode ?? "unknown", hint: "" };
  }
}

/** Tone for a pod row: like statusTone, but a Running pod whose
 * containers aren't all ready needs attention too. */
export function podTone(p: { status: string; ready: string }): Tone {
  const tone = statusTone(p.status);
  const [ready, total] = p.ready.split("/").map(Number);
  if (tone === "ok" && p.status === "Running" && total > 0 && ready < total) return "warn";
  return tone;
}
