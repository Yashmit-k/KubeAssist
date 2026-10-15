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
};

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
  detector: DetectorResult;
  userQuestion?: string;
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
