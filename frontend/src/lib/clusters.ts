import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { useParams } from "react-router";
import { api, isAgentNotConnected, type ClusterTarget } from "./api";
import type { ClusterStatus, User } from "./types";

/**
 * Accounts: the browser holds no secrets. Sign-in sets an HttpOnly
 * session cookie, and the backend lists and authorizes the user's own
 * clusters — so they follow the user to any browser.
 */
export function useMe() {
  return useQuery<User | null>({ queryKey: ["me"], queryFn: api.me, staleTime: 60_000, retry: 1 });
}

/** The signed-in user's registered clusters (empty when signed out). */
export function useMyClusters() {
  const me = useMe();
  return useQuery<ClusterStatus[]>({
    queryKey: ["clusters", me.data?.id ?? null],
    queryFn: api.clusters,
    enabled: !!me.data,
    refetchInterval: 15_000,
  });
}

export const LOCAL = "local";

export type ActiveCluster = {
  key: string; // "local" or a remote cluster id; used in URLs
  label: string;
  target: ClusterTarget | null; // null for a remote cluster while signed out (or still checking)
  isRemote: boolean;
};

/** Resolves the :cluster route param into an API target. */
export function useActiveCluster(): ActiveCluster {
  const { cluster = LOCAL } = useParams();
  const me = useMe();
  if (cluster === LOCAL) {
    return { key: LOCAL, label: "Local cluster", target: { kind: "local" }, isRemote: false };
  }
  return {
    key: cluster,
    label: cluster,
    target: me.data ? { kind: "remote", clusterId: cluster } : null,
    isRemote: true,
  };
}

/** Stable query-key fragment for a target. */
export function targetKey(t: ClusterTarget | null): string {
  return !t ? "none" : t.kind === "local" ? LOCAL : `remote:${t.clusterId}`;
}

/** How often to check a remote cluster's status while its agent is away. */
export const AGENT_POLL_MS = 3000;

/**
 * Recovers a page whose requests failed because the remote cluster's
 * agent isn't connected (503 agent_not_connected). While any of `errors`
 * is that error, polls the cluster's status; once it reports connected,
 * refetches every query of this target and calls `onConnected` (to
 * re-run a mutation, say). Returns whether the page is waiting.
 */
export function useAgentWait(target: ClusterTarget | null, errors: unknown[], onConnected?: () => void): boolean {
  const qc = useQueryClient();
  const clusterId = target?.kind === "remote" ? target.clusterId : null;
  const waiting = !!clusterId && errors.some(isAgentNotConnected);
  const status = useQuery({
    queryKey: ["remote-status", clusterId],
    queryFn: () => api.remoteStatus(clusterId!),
    enabled: waiting,
    refetchInterval: waiting ? AGENT_POLL_MS : false,
    retry: false,
  });
  const cb = useRef(onConnected);
  cb.current = onConnected;

  const key = targetKey(target);
  const connected = !!status.data?.connected;
  // Keyed on dataUpdatedAt so that if the agent dropped again before our
  // refetch landed, the next connected poll tries once more.
  useEffect(() => {
    if (!waiting || !connected) return;
    void qc.invalidateQueries({ predicate: (q) => q.queryKey[1] === key });
    cb.current?.();
  }, [waiting, connected, status.dataUpdatedAt, qc, key]);

  return waiting;
}
