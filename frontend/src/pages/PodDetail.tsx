import { useQuery } from "@tanstack/react-query";
import { RefreshCw, ScrollText, Stethoscope } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router";
import AgentWaiting from "../components/AgentWaiting";
import RemoteGate from "../components/RemoteGate";
import RolloutCard from "../components/RolloutCard";
import { Badge, buttonClass, Card, CopyButton, cx, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { api, type ClusterTarget } from "../lib/api";
import { targetKey, useActiveCluster, useAgentWait } from "../lib/clusters";
import { age, failureLabel, failureTone } from "../lib/format";
import type { ContainerInfo, ContainerState, Snapshot } from "../lib/types";

function stateText(s?: ContainerState | null): string {
  if (!s) return "—";
  const bits: string[] = [s.phase];
  if (s.reason) bits.push(s.reason);
  if (s.exitCode != null) bits.push(`exit ${s.exitCode}`);
  return bits.join(" · ");
}

function stateTone(s: ContainerState, ready: boolean) {
  if (s.phase === "running") return ready ? "ok" : "warn";
  if (s.phase === "terminated" && s.exitCode === 0) return "muted";
  return "bad";
}

function ContainersTable({ title, containers }: { title: string; containers: ContainerInfo[] }) {
  return (
    <Card title={title} bodyClassName="p-0">
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead className="border-b border-line text-xs text-muted">
            <tr>
              <th className="px-4 py-2 font-medium">Container</th>
              <th className="px-4 py-2 font-medium">State</th>
              <th className="px-4 py-2 font-medium">Last termination</th>
              <th className="px-4 py-2 text-right font-medium">Restarts</th>
              <th className="px-4 py-2 font-medium">Memory limit</th>
            </tr>
          </thead>
          <tbody>
            {containers.map((c) => (
              <tr key={c.name} className="border-b border-line align-top last:border-0">
                <td className="px-4 py-2.5">
                  <div className="font-medium">{c.name}</div>
                  <div className="max-w-72 truncate font-mono text-xs text-muted" title={c.image}>
                    {c.image}
                  </div>
                </td>
                <td className="px-4 py-2.5">
                  <Badge tone={stateTone(c.state, c.ready)}>{stateText(c.state)}</Badge>
                  {c.state.message && <div className="mt-1 max-w-sm text-xs break-words text-muted">{c.state.message}</div>}
                </td>
                <td className="px-4 py-2.5 text-xs text-muted">{stateText(c.lastState)}</td>
                <td className={cx("px-4 py-2.5 text-right tabular-nums", c.restartCount > 0 ? "text-bad" : "text-muted")}>{c.restartCount}</td>
                <td className="px-4 py-2.5 font-mono text-xs text-muted">{c.resources.limitsMemory ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {containers.some((c) => c.probes.liveness || c.probes.readiness || c.probes.startup) && (
        <div className="space-y-1 border-t border-line px-4 py-3 text-xs text-muted">
          {containers.flatMap((c) =>
            (["liveness", "readiness", "startup"] as const)
              .filter((k) => c.probes[k])
              .map((k) => (
                <div key={c.name + k}>
                  <span className="font-medium text-fg">{c.name}</span> {k}: <span className="font-mono">{c.probes[k]}</span>
                </div>
              )),
          )}
        </div>
      )}
    </Card>
  );
}

function LogsCard({ target, ns, name, snap }: { target: ClusterTarget; ns: string; name: string; snap: Snapshot }) {
  const all = [...(snap.initContainers ?? []), ...snap.containers];
  const failing = all.find((c) => !c.ready && c.restartCount > 0) ?? all.find((c) => !c.ready) ?? all[0];
  const [container, setContainer] = useState(failing?.name ?? "");
  const [previous, setPrevious] = useState((failing?.restartCount ?? 0) > 0);
  const logs = useQuery({
    queryKey: ["logs", targetKey(target), ns, name, container, previous],
    queryFn: () => api.logs(target, ns, name, container, previous),
    enabled: !!container,
    retry: false,
  });
  const logsWaiting = useAgentWait(target, [logs.error]);

  if (!all.length) return null;
  const text = logs.data?.logs ?? "";

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <ScrollText className="size-4" /> Logs
        </span>
      }
      actions={
        <>
          <select className="rounded-md border border-line bg-surface px-2 py-1 text-xs" value={container} onChange={(e) => setContainer(e.target.value)}>
            {all.map((c) => (
              <option key={c.name} value={c.name}>
                {c.name}
              </option>
            ))}
          </select>
          <label className="flex items-center gap-1.5 text-xs text-muted">
            <input type="checkbox" checked={previous} onChange={(e) => setPrevious(e.target.checked)} />
            previous instance
          </label>
          <button className="text-muted hover:text-fg" title="Reload logs" onClick={() => logs.refetch()}>
            <RefreshCw className={cx("size-3.5", logs.isFetching && "animate-spin")} />
          </button>
          {text && <CopyButton text={text} />}
        </>
      }
      bodyClassName="p-0"
    >
      {logs.isLoading ? (
        <div className="p-4">
          <Spinner label="Fetching logs…" />
        </div>
      ) : logsWaiting ? (
        <div className="p-4">
          <AgentWaiting clusterId={target.kind === "remote" ? target.clusterId : ""} />
        </div>
      ) : logs.isError ? (
        <div className="p-4">
          <ErrorBox error={logs.error} />
        </div>
      ) : text ? (
        <pre className="max-h-96 overflow-auto bg-code-bg px-4 py-3 font-mono text-[12px] leading-relaxed whitespace-pre-wrap text-code-fg">{text}</pre>
      ) : (
        <div className="px-4 py-6 text-sm text-muted">{logs.data?.unavailableReason ?? "No log output."}</div>
      )}
    </Card>
  );
}

export default function PodDetail() {
  const { ns = "", name = "" } = useParams();
  const cluster = useActiveCluster();
  const target = cluster.target;
  const snap = useQuery({
    queryKey: ["pod", targetKey(target), ns, name],
    queryFn: () => api.pod(target!, ns, name),
    enabled: !!target,
    refetchInterval: 20_000,
  });
  const waiting = useAgentWait(target, [snap.error]);

  if (!target) return <RemoteGate clusterId={cluster.key} />;
  const base = `/c/${encodeURIComponent(cluster.key)}`;
  const crumbs = (
    <>
      <Link to={`${base}/pods`} className="hover:text-fg">
        Pods
      </Link>{" "}
      /{" "}
      <Link to={`${base}/pods?ns=${encodeURIComponent(ns)}`} className="hover:text-fg">
        {ns}
      </Link>
    </>
  );

  if (snap.isLoading) return <Spinner label="Collecting pod evidence…" />;
  if (snap.isError)
    return (
      <>
        <PageHeader title={name} crumbs={crumbs} />
        {waiting ? <AgentWaiting clusterId={cluster.key} /> : <ErrorBox error={snap.error} title="Could not load this pod" />}
      </>
    );
  const s = snap.data!;
  const det = s.detector;
  const failingConditions = s.conditions.filter((c) => c.status !== "True");

  return (
    <>
      <PageHeader
        title={name}
        crumbs={crumbs}
        subtitle={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <span>phase {s.pod.phase}</span>
            {s.pod.nodeName && <span>node {s.pod.nodeName}</span>}
            {s.owners?.deployment && <span>deployment/{s.owners.deployment.name}</span>}
            <span>collected {age(s.collectedAt)} ago</span>
          </span>
        }
        actions={
          <Link to="diagnose" className={buttonClass("primary")}>
            <Stethoscope className="size-4" /> Diagnose
          </Link>
        }
      />

      <div className="space-y-4">
        <Card>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted">Detector:</span>
            <Badge tone={failureTone(det.failureType)}>{failureLabel(det.failureType)}</Badge>
            <span className="text-xs text-faint">confidence {Math.round(det.confidence * 100)}%</span>
          </div>
          {det.evidence.length > 0 && (
            <ul className="mt-2 space-y-1">
              {det.evidence.map((e, i) => (
                <li key={i} className="font-mono text-[12.5px] break-words text-muted">
                  {e}
                </li>
              ))}
            </ul>
          )}
          {s.pod.reason && (
            <div className="mt-2 text-sm">
              <span className="text-muted">Pod reason:</span> {s.pod.reason} {s.pod.message && <span className="text-muted">— {s.pod.message}</span>}
            </div>
          )}
        </Card>

        {s.rollout && <RolloutCard r={s.rollout} namespace={s.pod.namespace} failing={det.failureType !== "Healthy"} />}

        {s.initContainers && s.initContainers.length > 0 && <ContainersTable title="Init containers" containers={s.initContainers} />}
        {s.containers.length > 0 ? (
          <ContainersTable title="Containers" containers={s.containers} />
        ) : (
          <Card title="Containers">
            <p className="text-sm text-muted">No container has been created yet.</p>
          </Card>
        )}

        <div className="grid gap-4 lg:grid-cols-5">
          <Card title={`Events (${s.events.length})`} className="lg:col-span-3" bodyClassName="p-0">
            {s.events.length === 0 ? (
              <Empty title="No recent events" />
            ) : (
              <ul className="max-h-96 divide-y divide-line overflow-auto">
                {s.events.map((e, i) => (
                  <li key={i} className="px-4 py-2.5 text-sm">
                    <div className="flex items-center gap-2">
                      <Badge tone={e.type === "Warning" ? "warn" : "muted"}>{e.reason}</Badge>
                      {e.count && e.count > 1 ? <span className="text-xs text-faint">×{e.count}</span> : null}
                      <span className="ml-auto text-xs text-faint">{age(e.lastTimestamp)}</span>
                    </div>
                    <div className="mt-1 text-xs break-words text-muted">{e.message}</div>
                  </li>
                ))}
              </ul>
            )}
          </Card>
          <Card title="Conditions" className="lg:col-span-2">
            <ul className="space-y-1.5 text-sm">
              {s.conditions.map((c) => (
                <li key={c.type} className="flex items-center justify-between gap-2">
                  <span>{c.type}</span>
                  <Badge tone={c.status === "True" ? "ok" : "bad"}>{c.status}</Badge>
                </li>
              ))}
            </ul>
            {failingConditions.some((c) => c.message) && (
              <div className="mt-3 space-y-1 border-t border-line pt-3 text-xs text-muted">
                {failingConditions
                  .filter((c) => c.message)
                  .map((c) => (
                    <div key={c.type}>
                      <span className="font-medium text-fg">{c.type}:</span> {c.message}
                    </div>
                  ))}
              </div>
            )}
          </Card>
        </div>

        <LogsCard target={target} ns={ns} name={name} snap={s} />
      </div>
    </>
  );
}
