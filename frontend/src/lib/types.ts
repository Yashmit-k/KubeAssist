// Mirrors backend-go/internal/model and /schemas (schemaVersion 1.1.0).

export type FailureType =
  | "Healthy"
  | "CrashLoopBackOff"
  | "ProbeFailure"
  | "ImagePullBackOff"
  | "OOMKilled"
  | "CreateContainerConfigError"
  | "FailedScheduling"
  | "ServiceSelectorMismatch"
  | "Evicted"
  | "InitContainerFailure"
  | "VolumeMountFailure"
  | "ReadinessFailure"
  | "Unknown";

export type Health = {
  status: string;
  version: string;
  localMode: boolean;
  relay: boolean;
  ai: "ok" | "unavailable" | "disabled";
  store: "ok" | "unavailable" | "disabled";
  features: string[];
  /** Sign-in methods ("github", "dev"); empty when accounts are off. */
  authProviders?: string[];
};

export type User = {
  id: string;
  provider: string;
  login: string;
  name?: string;
  avatarUrl?: string;
};

export type ClusterInfo = { name: string; serverVersion: string };

export type ClusterStatus = {
  clusterId: string;
  connected: boolean;
  connectedAt?: string;
};

export type RegisteredCluster = {
  clusterId: string;
  token: string;
  relayUrl: string;
  manifest: string;
};

export type PodSummary = {
  name: string;
  namespace: string;
  phase: string;
  status: string;
  ready: string;
  restartCount: number;
  waitingReason?: string;
  nodeName?: string;
  createdAt: string;
};

export type ContainerState = {
  phase: "waiting" | "running" | "terminated";
  reason?: string;
  message?: string;
  exitCode?: number | null;
  startedAt?: string;
  finishedAt?: string;
};

export type ContainerInfo = {
  name: string;
  image: string;
  ready: boolean;
  restartCount: number;
  state: ContainerState;
  lastState?: ContainerState | null;
  resources: { limitsMemory?: string; requests?: Record<string, string>; limits?: Record<string, string> };
  probes: { liveness?: string; readiness?: string; startup?: string };
};

export type KubeEvent = {
  type: string;
  reason: string;
  message: string;
  count?: number;
  lastTimestamp?: string;
};

export type DetectorResult = {
  failureType: FailureType;
  confidence: number;
  evidence: string[];
  searchTerms: string[];
  relatedHints?: string[];
  fix?: FixPlan;
};

/** Evidence-derived fix: exact values (facts) and ready-to-run steps. */
export type FixPlan = {
  facts?: Record<string, string>;
  steps?: FixStep[];
};

export type FixStep = { description: string; command?: string | null; isWriteAction: boolean };

export type ScanPod = { namespace: string; name: string; status: string; ready: string; restartCount: number };

/** One root cause from a health scan and every pod it breaks. */
export type ScanGroup = {
  key: string;
  title: string;
  failureType: FailureType;
  pods: ScanPod[];
  evidence: string[];
  fix?: FixPlan;
};

export type ScanResult = {
  namespace?: string;
  scannedAt: string;
  totalPods: number;
  checked: number;
  truncated: boolean;
  healthy: number;
  groups: ScanGroup[];
  errors: { namespace: string; name: string; error: string }[];
};

export type Verdict = "fixed" | "starting" | "still_failing" | "new_problem" | "not_found";

export type VerifyResult = {
  verdict: Verdict;
  message: string;
  pod?: ScanPod;
  replaced: boolean;
  failureType?: FailureType;
  evidence: string[];
  fix?: FixPlan;
  checkedAt: string;
};

export type TemplateChange = { field: string; container?: string; name?: string; from?: string; to?: string };

/** The owning Deployment's revision history relative to a pod. */
export type Rollout = {
  workload: string;
  revision: number;
  rolledOutAt: string;
  latest: boolean;
  previousRevision?: number;
  previousCreatedAt?: string | null;
  changes: TemplateChange[];
};

export type AlertKind = "slack" | "discord" | "webhook";

export type AlertCause = { key: string; title: string; failureType: FailureType; pods: string[]; fix?: string };

export type AlertConfig = {
  configured: boolean;
  kind?: AlertKind;
  url?: string; // masked
  enabled: boolean;
  updatedAt?: string;
  interval: string;
  status: {
    lastScanAt?: string;
    lastScanError?: string;
    baseline: boolean;
    active: (AlertCause & { since: string })[];
    lastNotifiedAt?: string;
    lastDeliveryError?: string;
  };
};

export type ChatMessage = { role: "user" | "assistant"; content: string };

export type ChatToolUse = { name: string; arguments: Record<string, unknown>; summary: string; ok: boolean };

export type ChatReply = { reply: string; mode: "llm" | "rules"; tools: ChatToolUse[] };

export type VerifyRequest = { failureType: string; owner?: string; serviceName?: string };

export type Snapshot = {
  schemaVersion: string;
  cluster: ClusterInfo;
  pod: {
    name: string;
    namespace: string;
    phase: string;
    reason?: string;
    message?: string;
    nodeName?: string;
    labels?: Record<string, string>;
    ownerRefs?: { kind: string; name: string }[];
  };
  initContainers?: ContainerInfo[];
  containers: ContainerInfo[];
  conditions: { type: string; status: string; reason?: string; message?: string }[];
  events: KubeEvent[];
  logs: { current: string | null; previous: string | null; truncated: boolean; unavailableReason: string | null };
  owners?: { replicaSet?: { name: string } | null; deployment?: { name: string; strategy?: string } | null };
  related?: {
    services?: { name: string; selector: Record<string, string>; endpointsReady: number }[];
    configMapRefs?: { name: string; keysPresent?: string[]; keysMissing?: string[] }[];
  };
  rollout?: Rollout | null;
  detector: DetectorResult;
  userQuestion?: string;
  serviceName?: string | null;
  collectedAt: string;
};

export type ResolutionStep = {
  order: number;
  description: string;
  command?: string | null;
  isWriteAction?: boolean;
};

export type DocRef = { title: string; url: string; section?: string; snippet?: string };

export type Diagnosis = {
  schemaVersion: string;
  failureType: FailureType;
  issueSummary: string;
  observedEvidence: string[];
  probableRootCause: string;
  reasoning: string;
  confidenceScore: number;
  resolutionSteps: ResolutionStep[];
  verificationCommands: string[];
  rollbackGuidance: string;
  documentationReferences: DocRef[];
  uncertaintyNotes: string[];
  mode?: "full_rag" | "mock_llm" | "detector_only_stub";
};

/** What the Go API returns from troubleshoot: Diagnosis plus Go-side metadata. */
export type TroubleshootResult = Diagnosis & {
  investigationId?: string;
  detector: DetectorResult;
  aiError?: string;
};

export type InvestigationSummary = {
  id: string;
  clusterId: string;
  namespace: string;
  podName: string;
  failureType: FailureType;
  issueSummary: string;
  confidenceScore: number;
  mode: string;
  createdAt: string;
};

export type Investigation = {
  id: string;
  clusterId: string;
  namespace: string;
  podName: string;
  snapshot: Snapshot;
  diagnosis: Diagnosis;
  createdAt: string;
};

export type StageEvent = { stage: string; message?: string };
