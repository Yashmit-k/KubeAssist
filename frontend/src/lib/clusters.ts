import { useQuery } from "@tanstack/react-query";
import { useParams } from "react-router";
import { api, type ClusterTarget } from "./api";
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
