import { useSyncExternalStore } from "react";
import { useParams } from "react-router";
import type { ClusterTarget } from "./api";

/**
 * Remote cluster credentials live only in this browser (localStorage).
 * The backend stores just a hash of each token and never returns it
 * again, so this list is the user's own record of which clusters they
 * connected — there is deliberately no server-side "list my clusters"
 * without accounts, since that would let anyone enumerate everyone's.
 */
export type SavedCluster = { clusterId: string; token: string; addedAt: string };

const KEY = "kubeassist.clusters.v1";
const listeners = new Set<() => void>();
let cache: SavedCluster[] | null = null;

function read(): SavedCluster[] {
  if (cache) return cache;
  try {
    const raw = localStorage.getItem(KEY);
    const parsed = raw ? (JSON.parse(raw) as SavedCluster[]) : [];
    cache = Array.isArray(parsed) ? parsed.filter((c) => c && c.clusterId && c.token) : [];
  } catch {
    cache = [];
  }
  return cache;
}

function write(next: SavedCluster[]) {
  cache = next;
  try {
    localStorage.setItem(KEY, JSON.stringify(next));
  } catch {
    /* storage unavailable (private mode): keep in memory for this session */
  }
  listeners.forEach((l) => l());
}

export const savedClusters = {
  list: read,
  get: (id: string) => read().find((c) => c.clusterId === id),
  add(clusterId: string, token: string) {
    write([...read().filter((c) => c.clusterId !== clusterId), { clusterId, token, addedAt: new Date().toISOString() }]);
  },
  remove(clusterId: string) {
    write(read().filter((c) => c.clusterId !== clusterId));
  },
  subscribe(l: () => void) {
    listeners.add(l);
    const onStorage = (e: StorageEvent) => {
      if (e.key === KEY) {
        cache = null;
        l();
      }
    };
    window.addEventListener("storage", onStorage);
    return () => {
      listeners.delete(l);
      window.removeEventListener("storage", onStorage);
    };
  },
};

export function useSavedClusters(): SavedCluster[] {
  return useSyncExternalStore(savedClusters.subscribe, savedClusters.list, savedClusters.list);
}

export const LOCAL = "local";

export type ActiveCluster = {
  key: string; // "local" or a remote cluster id; used in URLs
  label: string;
  target: ClusterTarget | null; // null when a remote cluster's token isn't saved here
  isRemote: boolean;
};

/** Resolves the :cluster route param into an API target. */
export function useActiveCluster(): ActiveCluster {
  const { cluster = LOCAL } = useParams();
  const saved = useSavedClusters();
  if (cluster === LOCAL) {
    return { key: LOCAL, label: "Local cluster", target: { kind: "local" }, isRemote: false };
  }
  const entry = saved.find((c) => c.clusterId === cluster);
  return {
    key: cluster,
    label: cluster,
    target: entry ? { kind: "remote", clusterId: entry.clusterId, token: entry.token } : null,
    isRemote: true,
  };
}

/** Stable query-key fragment for a target (never includes the token). */
export function targetKey(t: ClusterTarget | null): string {
  return !t ? "none" : t.kind === "local" ? LOCAL : `remote:${t.clusterId}`;
}
