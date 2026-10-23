import { useMutation, useQuery } from "@tanstack/react-query";
import { CircleCheck, Radar, RefreshCw, Stethoscope } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import FixSteps from "../components/FixSteps";
import RemoteGate from "../components/RemoteGate";
import { Badge, Button, buttonClass, Card, Empty, ErrorBox, PageHeader } from "../components/ui";
import { VerifyOutcome } from "../components/VerifyFix";
import { api, type ClusterTarget } from "../lib/api";
import { targetKey, useActiveCluster } from "../lib/clusters";
import { dateTime, failureLabel, failureTone } from "../lib/format";
import type { ScanGroup, VerifyResult } from "../lib/types";

/** Group checks cover at most this many pods, one request each. */
const MAX_GROUP_CHECKS = 10;

function Stat({ label, value, tone }: { label: string; value: number | string; tone?: "bad" | "ok" }) {
  return (
    <div className="rounded-xl border border-line bg-surface px-4 py-3">
      <div className={tone === "bad" ? "text-2xl font-semibold text-bad" : tone === "ok" ? "text-2xl font-semibold text-ok" : "text-2xl font-semibold"}>
        {value}
      </div>
      <div className="text-xs text-muted">{label}</div>
    </div>
  );
}

/** Re-checks every pod of a group after the user ran its fix. */
function GroupCheck({ target, group }: { target: ClusterTarget; group: ScanGroup }) {
  const pods = group.pods.slice(0, MAX_GROUP_CHECKS);
  const check = useMutation({
    mutationFn: async () => {
      const out: { pod: string; result: VerifyResult }[] = [];
      for (const p of pods) {
        const result = await api.verify(target, p.namespace, p.name, { failureType: group.failureType, owner: group.fix?.facts?.owner });
        out.push({ pod: p.name, result });
      }
      return out;
    },
  });
  const fixed = check.data?.filter((c) => c.result.verdict === "fixed").length ?? 0;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button busy={check.isPending} onClick={() => check.mutate()}>
          {!check.isPending && <RefreshCw className="size-4" />}
          {check.data ? "Check again" : "I ran the fix — did it work?"}
        </Button>
        {check.data && (
          <span className="text-sm text-muted">
            {fixed}/{check.data.length} pod{check.data.length === 1 ? "" : "s"} fixed
          </span>
        )}
      </div>
      {check.isError && <ErrorBox error={check.error} />}
      {check.data?.map((c) => (
        <div key={c.pod} className="rounded-lg border border-line px-3 py-2.5">
          <VerifyOutcome r={c.result} />
        </div>
      ))}
    </div>
  );
}

function GroupCard({ target, group, base }: { target: ClusterTarget; group: ScanGroup; base: string }) {
  const first = group.pods[0];
  const podHref = (ns: string, name: string) => `${base}/pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;
  return (
    <Card
      title={
        <span className="flex flex-wrap items-center gap-2">
          <Badge tone={failureTone(group.failureType)}>{failureLabel(group.failureType)}</Badge>
          <span>{group.title}</span>
        </span>
      }
      actions={
        <span className="text-xs whitespace-nowrap text-muted">
          {group.pods.length} pod{group.pods.length === 1 ? "" : "s"}
        </span>
      }
    >
      <div className="space-y-4">
        <div className="flex flex-wrap gap-1.5">
          {group.pods.map((p) => (
            <Link
              key={p.namespace + "/" + p.name}
              to={podHref(p.namespace, p.name)}
              className="rounded-md border border-line px-2 py-1 font-mono text-[11.5px] hover:border-accent/50 hover:text-accent"
              title={`${p.status} · ready ${p.ready} · restarts ${p.restartCount}`}
            >
              {p.namespace}/{p.name}
            </Link>
          ))}
        </div>

        {group.evidence.length > 0 && (
          <ul className="space-y-1">
            {group.evidence.map((e, i) => (
              <li key={i} className="font-mono text-[12px] break-words text-muted">
                • {e}
              </li>
            ))}
          </ul>
        )}

        {group.fix?.steps?.length ? (
          <div>
            <div className="mb-2 text-xs font-semibold uppercase tracking-wider text-muted">
              Fix{group.pods.length > 1 ? ` — once, for all ${group.pods.length} pods` : ""}
            </div>
            <FixSteps steps={group.fix.steps} />
          </div>
        ) : (
          <p className="text-sm text-muted">No exact fix could be derived from the evidence — open the full diagnosis.</p>
        )}

        <div className="flex flex-wrap items-start gap-3 border-t border-line pt-3">
          <div className="min-w-0 flex-1">
            <GroupCheck target={target} group={group} />
          </div>
          <Link to={`${podHref(first.namespace, first.name)}/diagnose?run=1`} className={buttonClass("ghost", "text-sm")}>
            <Stethoscope className="size-4" /> Full AI diagnosis
          </Link>
        </div>
      </div>
    </Card>
  );
}

/** Cluster health scan: every unhealthy pod, grouped by root cause, each
 * group with one exact fix. Detector-only, so it's fast and free. */
export default function HealthScan() {
  const cluster = useActiveCluster();
  const target = cluster.target;
  const [namespace, setNamespace] = useState("");
  const namespaces = useQuery({
    queryKey: ["namespaces", targetKey(target)],
    queryFn: () => api.namespaces(target!),
    enabled: !!target,
  });
  const scan = useMutation({ mutationFn: () => api.scan(target!, namespace || undefined) });

  if (!target) return <RemoteGate clusterId={cluster.key} />;
  const base = `/c/${encodeURIComponent(cluster.key)}`;
  const r = scan.data;
  const broken = r?.groups.reduce((n, g) => n + g.pods.length, 0) ?? 0;

  return (
    <>
      <PageHeader
        title="Health scan"
        subtitle="Finds every unhealthy pod, groups them by root cause, and gives one exact fix per cause."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label="Namespace"
              className="rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm"
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
            >
              <option value="">All namespaces</option>
              {namespaces.data?.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
            <Button variant="primary" busy={scan.isPending} onClick={() => scan.mutate()}>
              {!scan.isPending && <Radar className="size-4" />}
              {scan.isPending ? "Scanning…" : r ? "Scan again" : "Run scan"}
            </Button>
          </div>
        }
      />

      {scan.isError && <ErrorBox error={scan.error} title="Scan failed" />}

      {!r && !scan.isPending && !scan.isError && (
        <Card>
          <Empty icon={<Radar className="size-8" />} title="Scan this cluster for problems">
            Checks every pod that isn't cleanly running, classifies what's wrong, and groups pods that share a cause — so you fix it once. No AI call is
            made, so it takes seconds.
          </Empty>
        </Card>
      )}

      {r && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat label="pods in scope" value={r.totalPods} />
            <Stat label="unhealthy pods" value={broken} tone={broken ? "bad" : "ok"} />
            <Stat label="root causes" value={r.groups.length} tone={r.groups.length ? "bad" : "ok"} />
            <Stat label="checked, turned out fine" value={r.healthy} />
          </div>
          <p className="text-xs text-muted">
            Scanned {r.namespace ? `namespace ${r.namespace}` : "all namespaces"} at {dateTime(r.scannedAt)}.
            {r.truncated && " Only the first 40 unhealthy pods were checked — scan a single namespace to see the rest."}
          </p>

          {r.groups.length === 0 ? (
            <Card>
              <Empty icon={<CircleCheck className="size-8 text-ok" />} title="No problems found">
                Every pod is running and ready. A Service that routes to nothing won't show here — diagnose its pod with the Service name to check that.
              </Empty>
            </Card>
          ) : (
            r.groups.map((g) => <GroupCard key={g.key} target={target} group={g} base={base} />)
          )}

          {r.errors.length > 0 && (
            <Card title="Pods that couldn't be checked">
              <ul className="space-y-1 text-sm">
                {r.errors.map((e) => (
                  <li key={e.namespace + "/" + e.name}>
                    <span className="font-mono text-xs">
                      {e.namespace}/{e.name}
                    </span>{" "}
                    <span className="text-muted">— {e.error}</span>
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      )}
    </>
  );
}
