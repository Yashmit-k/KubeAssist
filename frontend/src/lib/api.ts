import type {
  ClusterInfo,
  ClusterStatus,
  Health,
  Investigation,
  InvestigationSummary,
  PodSummary,
  RegisteredCluster,
  Snapshot,
  StageEvent,
  AlertConfig,
  FixOptions,
  FixPRResult,
  RepoAccess,
  RepoLink,
  AlertKind,
  ChatMessage,
  ChatReply,
  ScanResult,
  TroubleshootResult,
  User,
  VerifyRequest,
  VerifyResult,
} from "./types";

// Empty base = same origin. In dev, Vite proxies /api to the Go backend;
// in the container image, nginx does. Set VITE_API_BASE_URL only when the
// frontend is hosted separately from the API (e.g. Vercel + Fly.io).
const BASE = ((import.meta.env.VITE_API_BASE_URL as string | undefined) ?? "").replace(/\/$/, "");

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/**
 * A ClusterTarget is where a request goes: this backend's own cluster
 * ("local", under /api) or a remote cluster reached through its agent
 * (under /api/remote/{id}). Remote requests are authorized by the
 * HttpOnly session cookie — the browser never holds a cluster token.
 */
export type ClusterTarget = { kind: "local" } | { kind: "remote"; clusterId: string };

function prefix(t: ClusterTarget): string {
  return t.kind === "local" ? "/api" : `/api/remote/${encodeURIComponent(t.clusterId)}`;
}

// "include" so the session cookie also travels when the UI is hosted on
// a different origin of the same site (VITE_API_BASE_URL).
const CREDENTIALS: RequestCredentials = "include";

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const { headers, ...rest } = init;
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      ...rest,
      credentials: CREDENTIALS,
      headers: { ...(rest.body ? { "Content-Type": "application/json" } : {}), ...headers },
    });
  } catch {
    throw new ApiError(0, "network_error", "Cannot reach the KubeAssist API. Is the backend running?");
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let body: unknown = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    throw new ApiError(res.status, "invalid_json", `Unexpected response (${res.status}): ${text.slice(0, 160)}`);
  }
  if (!res.ok) {
    const err = (body ?? {}) as { code?: string; message?: string };
    throw new ApiError(res.status, err.code ?? `http_${res.status}`, err.message ?? `Request failed (${res.status})`);
  }
  return body as T;
}

const enc = encodeURIComponent;

export const api = {
  health: () => request<Health>("/api/health"),
  localCluster: () => request<ClusterInfo>("/api/cluster"),

  namespaces: (t: ClusterTarget) => request<string[]>(`${prefix(t)}/namespaces`),
  pods: (t: ClusterTarget, namespace?: string) =>
    request<PodSummary[]>(`${prefix(t)}/pods${namespace ? `?namespace=${enc(namespace)}` : ""}`),
  pod: (t: ClusterTarget, ns: string, name: string) =>
    request<Snapshot>(`${prefix(t)}/pods/${enc(ns)}/${enc(name)}`),
  logs: (t: ClusterTarget, ns: string, name: string, container: string, previous: boolean) =>
    request<{ logs: string; unavailableReason?: string }>(
      `${prefix(t)}/pods/${enc(ns)}/${enc(name)}/logs?container=${enc(container)}&previous=${previous}`,
    ),

  investigations: (t: ClusterTarget, namespace?: string) =>
    request<InvestigationSummary[]>(
      `${prefix(t)}/investigations?limit=100${namespace ? `&namespace=${enc(namespace)}` : ""}`,
    ),
  /** Health scan: classify every unhealthy pod and group by root cause. */
  scan: (t: ClusterTarget, namespace?: string) =>
    request<ScanResult>(`${prefix(t)}/scan${namespace ? `?namespace=${enc(namespace)}` : ""}`, { method: "POST" }),
  /** GitHub repository access (separate from sign-in) for fix pull requests. */
  repoAccess: () => request<RepoAccess>("/api/auth/github/repo-access"),
  disconnectRepoAccess: () => request<void>("/api/auth/github/repo-access", { method: "DELETE" }),
  repoAccessStartUrl: (includePrivate: boolean, next: string) =>
    `${BASE}/api/auth/github/repo-access/start?private=${includePrivate ? 1 : 0}&next=${enc(next)}`,
  repoLink: (clusterId: string) => request<{ link: RepoLink | null }>(`/api/remote/${enc(clusterId)}/repo`),
  saveRepoLink: (clusterId: string, body: { repo: string; branch?: string; path?: string }) =>
    request<{ link: RepoLink }>(`/api/remote/${enc(clusterId)}/repo`, { method: "PUT", body: JSON.stringify(body) }),
  deleteRepoLink: (clusterId: string) => request<void>(`/api/remote/${enc(clusterId)}/repo`, { method: "DELETE" }),
  fixOptions: (clusterId: string, ns: string, name: string) =>
    request<FixOptions>(`/api/remote/${enc(clusterId)}/pods/${enc(ns)}/${enc(name)}/fix-pr`),
  openFixPR: (clusterId: string, ns: string, name: string, optionId: string, inputs: Record<string, string>) =>
    request<FixPRResult>(`/api/remote/${enc(clusterId)}/pods/${enc(ns)}/${enc(name)}/fix-pr`, {
      method: "POST",
      body: JSON.stringify({ optionId, inputs }),
    }),

  /** Alerts for a connected cluster (owner only). */
  alerts: (clusterId: string) => request<AlertConfig>(`/api/remote/${enc(clusterId)}/alerts`),
  saveAlerts: (clusterId: string, body: { kind: AlertKind; url?: string; enabled: boolean }) =>
    request<AlertConfig>(`/api/remote/${enc(clusterId)}/alerts`, { method: "PUT", body: JSON.stringify(body) }),
  deleteAlerts: (clusterId: string) => request<void>(`/api/remote/${enc(clusterId)}/alerts`, { method: "DELETE" }),
  testAlert: (clusterId: string) => request<{ ok: boolean; error?: string }>(`/api/remote/${enc(clusterId)}/alerts/test`, { method: "POST" }),

  /** Ask about the cluster: the AI picks read-only tools, the backend runs them. */
  chat: (t: ClusterTarget, messages: ChatMessage[]) =>
    request<ChatReply>(`${prefix(t)}/chat`, { method: "POST", body: JSON.stringify({ messages }) }),
  /** "Did my fix work?" — re-checks the pod, or its workload's newest pod. */
  verify: (t: ClusterTarget, ns: string, name: string, body: VerifyRequest) =>
    request<VerifyResult>(`${prefix(t)}/pods/${enc(ns)}/${enc(name)}/verify`, { method: "POST", body: JSON.stringify(body) }),

  investigation: (t: ClusterTarget, id: string) =>
    request<Investigation>(`${prefix(t)}/investigations/${enc(id)}`),

  /** The signed-in user, or null when signed out. */
  me: async (): Promise<User | null> => {
    try {
      return await request<User>("/api/auth/me");
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return null;
      throw e;
    }
  },
  devLogin: (login: string) => request<User>("/api/auth/dev-login", { method: "POST", body: JSON.stringify({ login }) }),
  logout: () => request<void>("/api/auth/logout", { method: "POST" }),
  /** Full-page navigation target that starts an OAuth sign-in. */
  loginUrl: (provider: string, next: string) => `${BASE}/api/auth/${enc(provider)}/login?next=${enc(next)}`,

  /** The signed-in user's clusters. */
  clusters: () => request<ClusterStatus[]>("/api/clusters"),
  registerCluster: (clusterId: string) =>
    request<RegisteredCluster>("/api/clusters", { method: "POST", body: JSON.stringify({ clusterId }) }),
  remoteStatus: (clusterId: string) => request<ClusterStatus>(`/api/remote/${enc(clusterId)}/status`),
  revokeCluster: (clusterId: string) => request<void>(`/api/clusters/${enc(clusterId)}`, { method: "DELETE" }),
};

export type StreamHandlers = {
  onStage: (e: StageEvent) => void;
  onDone: (r: TroubleshootResult) => void;
  onError: (e: ApiError) => void;
};

/**
 * Runs a troubleshoot over Server-Sent Events. We use fetch + a stream
 * reader instead of EventSource because EventSource can only GET, and
 * the remote stream route is POST-only.
 */
export function troubleshootStream(
  t: ClusterTarget,
  ns: string,
  name: string,
  body: { question?: string; serviceName?: string },
  h: StreamHandlers,
): () => void {
  const ctrl = new AbortController();
  let finished = false;

  (async () => {
    let res: Response;
    try {
      res = await fetch(`${BASE}${prefix(t)}/pods/${enc(ns)}/${enc(name)}/troubleshoot/stream`, {
        method: "POST",
        credentials: CREDENTIALS,
        headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
        body: JSON.stringify(body),
        signal: ctrl.signal,
      });
    } catch {
      if (!ctrl.signal.aborted) h.onError(new ApiError(0, "network_error", "Cannot reach the KubeAssist API."));
      return;
    }
    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => "");
      let code = `http_${res.status}`;
      let message = `Request failed (${res.status})`;
      try {
        const j = JSON.parse(text) as { code?: string; message?: string };
        code = j.code ?? code;
        message = j.message ?? message;
      } catch {
        /* not JSON */
      }
      h.onError(new ApiError(res.status, code, message));
      return;
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        let sep: number;
        while ((sep = buf.indexOf("\n\n")) !== -1) {
          const raw = buf.slice(0, sep);
          buf = buf.slice(sep + 2);
          let event = "message";
          const data: string[] = [];
          for (const line of raw.split("\n")) {
            if (line.startsWith(":")) continue; // keepalive comment
            if (line.startsWith("event:")) event = line.slice(6).trim();
            else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
          }
          if (!data.length) continue;
          const payload = JSON.parse(data.join("\n"));
          if (event === "done") {
            finished = true;
            h.onDone(payload as TroubleshootResult);
          } else if (event === "error") {
            finished = true;
            h.onError(new ApiError(500, payload.code ?? "error", payload.message ?? "Troubleshoot failed"));
          } else {
            h.onStage(payload as StageEvent);
          }
        }
      }
      if (!finished && !ctrl.signal.aborted) {
        h.onError(new ApiError(0, "stream_closed", "The connection closed before the diagnosis finished."));
      }
    } catch (e) {
      if (!ctrl.signal.aborted) h.onError(new ApiError(0, "stream_error", e instanceof Error ? e.message : String(e)));
    }
  })();

  return () => ctrl.abort();
}
