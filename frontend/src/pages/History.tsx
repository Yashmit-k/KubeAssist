import { useQuery } from "@tanstack/react-query";
import { History as HistoryIcon } from "lucide-react";
import { Link, useParams } from "react-router";
import { useState } from "react";
import { diagnosisReport, reportFileName } from "../lib/report";
import type { Snapshot, VerifyResult } from "../lib/types";
import ReportButtons from "../components/ReportButtons";
import VerifyFix from "../components/VerifyFix";
import DiagnosisView from "../components/DiagnosisView";
import RemoteGate from "../components/RemoteGate";
import { Badge, Card, Confidence, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { api, ApiError } from "../lib/api";
import { targetKey, useActiveCluster } from "../lib/clusters";
import { age, dateTime, failureLabel, failureTone, modeLabel } from "../lib/format";

function StoreDisabled({ error }: { error: unknown }) {
  if (error instanceof ApiError && error.code === "store_unavailable") {
    return (
      <Empty icon={<HistoryIcon className="size-8" />} title="History is not enabled">
        The backend has no database configured. Set <code className="font-mono">DATABASE_URL</code> on the Go backend to keep investigations.
      </Empty>
    );
  }
  return <ErrorBox error={error} title="Could not load history" />;
}

export function HistoryList() {
  const cluster = useActiveCluster();
  const target = cluster.target;
  const list = useQuery({
    queryKey: ["investigations", targetKey(target)],
    queryFn: () => api.investigations(target!),
    enabled: !!target,
    retry: (n, e) => !(e instanceof ApiError && e.status < 500) && n < 2,
  });
  if (!target) return <RemoteGate clusterId={cluster.key} />;
  const base = `/c/${encodeURIComponent(cluster.key)}`;

  return (
    <>
      <PageHeader title="History" subtitle={`Past diagnoses for ${cluster.label}`} />
      <Card bodyClassName="p-0">
        {list.isLoading ? (
          <div className="p-6">
            <Spinner />
          </div>
        ) : list.isError ? (
          <div className="p-4">
            <StoreDisabled error={list.error} />
          </div>
        ) : !list.data?.length ? (
          <Empty icon={<HistoryIcon className="size-8" />} title="No investigations yet">
            Diagnose a pod and it will show up here.
          </Empty>
        ) : (
          <ul className="divide-y divide-line">
            {list.data.map((inv) => (
              <li key={inv.id}>
                <Link to={`${base}/history/${inv.id}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 hover:bg-surface-2/60">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate font-medium">{inv.podName}</span>
                      <span className="text-xs text-muted">{inv.namespace}</span>
                    </div>
                    <div className="truncate text-xs text-muted">{inv.issueSummary}</div>
                  </div>
                  <Badge tone={failureTone(inv.failureType)}>{failureLabel(inv.failureType)}</Badge>
                  <span className="hidden sm:block">
                    <Confidence value={inv.confidenceScore} />
                  </span>
                  <span className="w-16 text-right text-xs text-faint" title={dateTime(inv.createdAt)}>
                    {age(inv.createdAt)} ago
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </>
  );
}

/** The workload a stored snapshot's pod belongs to, for following it to
 * a replacement pod. Older investigations predate detector.fix. */
function ownerOf(snap: Snapshot): string | undefined {
  const fromFix = snap.detector?.fix?.facts?.owner;
  if (fromFix) return fromFix;
  if (snap.owners?.deployment?.name) return `deployment/${snap.owners.deployment.name}`;
  const ref = snap.pod.ownerRefs?.[0];
  return ref ? `${ref.kind.toLowerCase()}/${ref.name}` : undefined;
}

export function InvestigationDetail() {
  const { id = "" } = useParams();
  const cluster = useActiveCluster();
  const target = cluster.target;
  const [verified, setVerified] = useState<VerifyResult | null>(null);
  const inv = useQuery({
    queryKey: ["investigation", targetKey(target), id],
    queryFn: () => api.investigation(target!, id),
    enabled: !!target,
  });
  if (!target) return <RemoteGate clusterId={cluster.key} />;
  const base = `/c/${encodeURIComponent(cluster.key)}`;
  if (inv.isLoading) return <Spinner />;
  if (inv.isError) return <ErrorBox error={inv.error} title="Could not load investigation" />;
  const d = inv.data!;
  const snap = d.snapshot;

  return (
    <>
      <PageHeader
        title={d.podName}
        crumbs={
          <>
            <Link to={`${base}/history`} className="hover:text-fg">
              History
            </Link>{" "}
            / {d.namespace}
          </>
        }
        subtitle={`${dateTime(d.createdAt)} · ${modeLabel(d.diagnosis.mode).label}`}
        actions={
          <div className="flex flex-wrap items-center gap-3">
            <ReportButtons
              filename={reportFileName(d.namespace, d.podName, d.createdAt.slice(0, 10))}
              markdown={diagnosisReport({ cluster: cluster.label, namespace: d.namespace, pod: d.podName, diagnosis: d.diagnosis, question: snap.userQuestion || undefined, verify: verified, generatedAt: new Date() })}
            />
            <Link to={`${base}/pods/${encodeURIComponent(d.namespace)}/${encodeURIComponent(d.podName)}`} className="text-sm text-accent hover:underline">
              Open current pod →
            </Link>
          </div>
        }
      />
      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-3 lg:col-span-2">
          <DiagnosisView d={d.diagnosis} />
          {d.diagnosis.failureType !== "Healthy" && (
            <VerifyFix
              target={target}
              ns={d.namespace}
              name={d.podName}
              req={{ failureType: d.diagnosis.failureType, owner: ownerOf(snap), serviceName: snap.serviceName ?? undefined }}
              onResult={setVerified}
            />
          )}
        </div>
        <div className="space-y-4">
          {snap.userQuestion && (
            <Card title="Question asked">
              <p className="text-sm">{snap.userQuestion}</p>
            </Card>
          )}
          <Card title="Snapshot at the time">
            <dl className="space-y-1.5 text-sm">
              <div className="flex justify-between gap-2">
                <dt className="text-muted">Phase</dt>
                <dd>{snap.pod.phase}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className="text-muted">Containers</dt>
                <dd>{snap.containers.length}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className="text-muted">Events</dt>
                <dd>{snap.events.length}</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt className="text-muted">Cluster</dt>
                <dd className="truncate">{snap.cluster.name} {snap.cluster.serverVersion}</dd>
              </div>
            </dl>
            {snap.logs.previous || snap.logs.current ? (
              <details className="mt-3">
                <summary className="cursor-pointer text-xs text-muted">Logs sent for diagnosis (redacted)</summary>
                <pre className="mt-2 max-h-72 overflow-auto rounded-lg bg-code-bg p-3 font-mono text-[11px] whitespace-pre-wrap text-code-fg">
                  {snap.logs.previous ?? snap.logs.current}
                </pre>
              </details>
            ) : null}
          </Card>
        </div>
      </div>
    </>
  );
}
