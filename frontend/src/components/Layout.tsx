import { useQuery } from "@tanstack/react-query";
import { Boxes, History, LogIn, PlugZap, Server } from "lucide-react";
import { NavLink, Outlet, useNavigate, useParams } from "react-router";
import { api } from "../lib/api";
import { LOCAL, useMe, useMyClusters } from "../lib/clusters";
import type { Health } from "../lib/types";
import { UserBadge } from "./SignIn";
import { cx, Dot } from "./ui";

export function useHealth() {
  return useQuery({ queryKey: ["health"], queryFn: api.health, refetchInterval: 30_000, retry: 1 });
}

function ClusterSwitcher({ health }: { health?: Health }) {
  const { cluster } = useParams();
  const mine = useMyClusters();
  const navigate = useNavigate();
  const options = [
    ...(health?.localMode ? [{ value: LOCAL, label: "Local cluster" }] : []),
    ...(mine.data ?? []).map((c) => ({ value: c.clusterId, label: c.clusterId })),
  ];
  if (cluster && !options.some((o) => o.value === cluster)) options.push({ value: cluster, label: cluster });

  if (!options.length) return null;
  return (
    <label className="block">
      <span className="mb-1 block text-[11px] font-medium uppercase tracking-wider text-faint">Cluster</span>
      <select
        className="w-full rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm"
        value={cluster ?? ""}
        onChange={(e) => navigate(`/c/${encodeURIComponent(e.target.value)}/pods`)}
      >
        {!cluster && <option value="">Select a cluster…</option>}
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function StatusRow({ label, state }: { label: string; state?: string }) {
  const tone = state === "ok" ? "ok" : state === "unavailable" ? "bad" : "muted";
  const text = state === "ok" ? "online" : state === "unavailable" ? "unreachable" : state === "disabled" ? "not configured" : "…";
  return (
    <div className="flex items-center justify-between text-xs">
      <span className="text-muted">{label}</span>
      <span className="flex items-center gap-1.5 text-muted">
        <Dot tone={tone} />
        {text}
      </span>
    </div>
  );
}

export default function Layout() {
  const { cluster } = useParams();
  const health = useHealth();
  const me = useMe();
  const accounts = (health.data?.authProviders?.length ?? 0) > 0;
  const base = cluster ? `/c/${encodeURIComponent(cluster)}` : null;

  const nav = [
    ...(base
      ? [
          { to: `${base}/pods`, label: "Pods", icon: Boxes },
          { to: `${base}/history`, label: "History", icon: History },
        ]
      : []),
    { to: "/connect", label: "Connect cluster", icon: PlugZap },
  ];

  return (
    <div className="flex min-h-full flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col gap-5 border-b border-line bg-surface px-4 py-4 md:sticky md:top-0 md:h-screen md:w-60 md:border-r md:border-b-0">
        <NavLink to="/" className="flex items-center gap-2 font-semibold tracking-tight">
          <span className="grid size-7 place-items-center rounded-lg bg-accent text-accent-fg">
            <Server className="size-4" />
          </span>
          KubeAssist
        </NavLink>

        <ClusterSwitcher health={health.data} />

        <nav className="flex gap-1 overflow-x-auto md:flex-col">
          {nav.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cx(
                  "flex items-center gap-2 rounded-lg px-2.5 py-1.5 text-sm whitespace-nowrap",
                  isActive ? "bg-accent-soft font-medium text-accent" : "text-muted hover:bg-surface-2 hover:text-fg",
                )
              }
            >
              <Icon className="size-4" />
              {label}
            </NavLink>
          ))}
        </nav>

        {me.data ? (
          <div className="border-t border-line pt-3 md:mt-auto">
            <UserBadge user={me.data} />
          </div>
        ) : (
          accounts &&
          me.data === null && (
            <NavLink to="/connect" className="flex items-center gap-2 border-t border-line pt-3 text-sm text-muted hover:text-fg md:mt-auto">
              <LogIn className="size-4" /> Sign in
            </NavLink>
          )
        )}

        <div className={cx("hidden space-y-1.5 border-t border-line pt-4 md:block", !(me.data || (accounts && me.data === null)) && "mt-auto")}>
          <StatusRow label="API" state={health.isError ? "unavailable" : health.data ? "ok" : undefined} />
          <StatusRow label="AI service" state={health.data?.ai} />
          <StatusRow label="History DB" state={health.data?.store} />
          {health.data && <div className="pt-1 text-[11px] text-faint">backend {health.data.version}</div>}
        </div>
      </aside>

      <main className="min-w-0 flex-1 px-4 py-6 md:px-8">
        <div className="mx-auto max-w-6xl">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
