import { useQuery } from "@tanstack/react-query";
import { Boxes, Radar, RefreshCw, Search, Stethoscope } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { api } from "../lib/api";
import { targetKey, useActiveCluster } from "../lib/clusters";
import { age, podTone } from "../lib/format";
import { Badge, Button, buttonClass, Card, cx, Dot, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import RemoteGate from "../components/RemoteGate";

export default function Dashboard() {
  const cluster = useActiveCluster();
  const [params, setParams] = useSearchParams();
  const namespace = params.get("ns") ?? "";
  const [filter, setFilter] = useState("");
  const [unhealthyOnly, setUnhealthyOnly] = useState(false);
  const target = cluster.target;

  const namespaces = useQuery({
    queryKey: ["namespaces", targetKey(target)],
    queryFn: () => api.namespaces(target!),
    enabled: !!target,
  });
  const pods = useQuery({
    queryKey: ["pods", targetKey(target), namespace],
    queryFn: () => api.pods(target!, namespace || undefined),
    enabled: !!target,
    refetchInterval: 15_000,
  });

  const rows = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (pods.data ?? []).filter(
      (p) =>
        (!q || p.name.toLowerCase().includes(q) || p.status.toLowerCase().includes(q)) &&
        (!unhealthyOnly || podTone(p) !== "ok"),
    );
  }, [pods.data, filter, unhealthyOnly]);

  const unhealthy = (pods.data ?? []).filter((p) => podTone(p) !== "ok").length;

  if (!target) return <RemoteGate clusterId={cluster.key} />;

  return (
    <>
      <PageHeader
        title="Pods"
        subtitle={
          pods.data
            ? `${pods.data.length} pods${namespace ? ` in ${namespace}` : " across all namespaces"} · ${unhealthy} need attention`
            : cluster.label
        }
        actions={
          <div className="flex flex-wrap gap-2">
            <Link to={`/c/${encodeURIComponent(cluster.key)}/scan`} className={buttonClass(unhealthy ? "primary" : "secondary")}>
              <Radar className="size-4" /> {unhealthy ? `Find root causes (${unhealthy})` : "Health scan"}
            </Link>
            <Button variant="ghost" onClick={() => pods.refetch()} busy={pods.isFetching} title="Refresh">
              {!pods.isFetching && <RefreshCw className="size-4" />}
              Refresh
            </Button>
          </div>
        }
      />

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <select
          aria-label="Namespace"
          className="rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm"
          value={namespace}
          onChange={(e) => setParams(e.target.value ? { ns: e.target.value } : {})}
        >
          <option value="">All namespaces</option>
          {namespaces.data?.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-faint" />
          <input
            className="w-full rounded-lg border border-line bg-surface py-1.5 pr-3 pl-8 text-sm"
            placeholder="Filter by name or status"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
        <label className="flex items-center gap-2 text-sm text-muted">
          <input type="checkbox" checked={unhealthyOnly} onChange={(e) => setUnhealthyOnly(e.target.checked)} />
          Needs attention only
        </label>
      </div>

      <Card bodyClassName="p-0">
        {pods.isLoading ? (
          <div className="p-6">
            <Spinner label="Loading pods…" />
          </div>
        ) : pods.isError ? (
          <div className="p-4">
            <ErrorBox error={pods.error} title="Could not list pods" />
          </div>
        ) : rows.length === 0 ? (
          <Empty icon={<Boxes className="size-8" />} title={pods.data?.length ? "No pods match the filter" : "No pods found"} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="border-b border-line text-xs text-muted">
                <tr>
                  <th className="px-4 py-2 font-medium">Name</th>
                  {!namespace && <th className="px-4 py-2 font-medium">Namespace</th>}
                  <th className="px-4 py-2 font-medium">Status</th>
                  <th className="px-4 py-2 font-medium">Ready</th>
                  <th className="px-4 py-2 text-right font-medium">Restarts</th>
                  <th className="px-4 py-2 text-right font-medium">Age</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {rows.map((p) => {
                  const tone = podTone(p);
                  const href = `/c/${encodeURIComponent(cluster.key)}/pods/${encodeURIComponent(p.namespace)}/${encodeURIComponent(p.name)}`;
                  return (
                    <tr key={`${p.namespace}/${p.name}`} className="border-b border-line last:border-0 hover:bg-surface-2/60">
                      <td className="max-w-80 px-4 py-2">
                        <Link to={href} className="flex items-center gap-2 font-medium hover:text-accent">
                          <Dot tone={tone} />
                          <span className="truncate">{p.name}</span>
                        </Link>
                      </td>
                      {!namespace && <td className="px-4 py-2 whitespace-nowrap text-muted">{p.namespace}</td>}
                      <td className="px-4 py-2">
                        <Badge tone={tone}>{p.status}</Badge>
                      </td>
                      <td className="px-4 py-2 tabular-nums text-muted">{p.ready}</td>
                      <td className={cx("px-4 py-2 text-right tabular-nums", p.restartCount > 0 ? "text-bad" : "text-muted")}>{p.restartCount}</td>
                      <td className="px-4 py-2 text-right tabular-nums text-muted">{age(p.createdAt)}</td>
                      <td className="px-4 py-2 text-right">
                        {tone !== "ok" && (
                          <Link
                            to={`${href}/diagnose?run=1`}
                            className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-accent hover:bg-accent-soft"
                          >
                            <Stethoscope className="size-3.5" /> Diagnose
                          </Link>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}
