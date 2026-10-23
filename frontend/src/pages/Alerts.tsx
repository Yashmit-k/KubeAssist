import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, BellOff, CircleAlert, CircleCheck, Send, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import RemoteGate from "../components/RemoteGate";
import { Badge, Button, Card, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { api } from "../lib/api";
import { useActiveCluster } from "../lib/clusters";
import { age, failureLabel, failureTone, goDuration } from "../lib/format";
import type { AlertKind } from "../lib/types";

const KINDS: { value: AlertKind; label: string; placeholder: string; help: string }[] = [
  {
    value: "slack",
    label: "Slack",
    placeholder: "https://hooks.slack.com/services/…",
    help: "In Slack: Apps → Incoming Webhooks → Add to a channel, then copy the Webhook URL.",
  },
  {
    value: "discord",
    label: "Discord",
    placeholder: "https://discord.com/api/webhooks/…",
    help: "In Discord: channel settings → Integrations → Webhooks → New Webhook → Copy Webhook URL.",
  },
  {
    value: "webhook",
    label: "Generic webhook (JSON)",
    placeholder: "https://example.com/kubeassist-hook",
    help: 'Receives a POST with JSON: {"event","cluster","new":[…],"resolved":[…],"url"}. Must be https.',
  },
];

/** Proactive alerts: KubeAssist scans this cluster on a schedule and
 * posts to a webhook when a problem starts or is resolved. */
export default function Alerts() {
  const cluster = useActiveCluster();
  const qc = useQueryClient();
  const clusterId = cluster.key;
  const enabledQuery = cluster.isRemote && !!cluster.target;
  const cfg = useQuery({ queryKey: ["alerts", clusterId], queryFn: () => api.alerts(clusterId), enabled: enabledQuery, refetchInterval: 15_000 });

  const [kind, setKind] = useState<AlertKind>("slack");
  const [url, setUrl] = useState("");
  const [on, setOn] = useState(true);
  useEffect(() => {
    if (cfg.data?.configured) {
      setKind(cfg.data.kind ?? "slack");
      setOn(cfg.data.enabled);
    }
  }, [cfg.data?.configured, cfg.data?.kind, cfg.data?.enabled]);

  const save = useMutation({
    mutationFn: () => api.saveAlerts(clusterId, { kind, url: url.trim() || undefined, enabled: on }),
    onSuccess: (data) => {
      qc.setQueryData(["alerts", clusterId], data);
      setUrl("");
    },
  });
  const test = useMutation({ mutationFn: () => api.testAlert(clusterId), onSettled: () => void qc.invalidateQueries({ queryKey: ["alerts", clusterId] }) });
  const remove = useMutation({
    mutationFn: () => api.deleteAlerts(clusterId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["alerts", clusterId] });
      setUrl("");
    },
  });

  if (!cluster.isRemote) {
    return (
      <>
        <PageHeader title="Alerts" />
        <Card>
          <Empty icon={<BellOff className="size-8" />} title="Alerts are for connected clusters">
            Alerts go to the account that owns a cluster. Connect a cluster through the agent (Connect cluster) to set them up.
          </Empty>
        </Card>
      </>
    );
  }
  if (!cluster.target) return <RemoteGate clusterId={clusterId} />;
  if (cfg.isLoading) return <Spinner />;
  if (cfg.isError) return <ErrorBox error={cfg.error} title="Could not load alerts" />;

  const c = cfg.data!;
  const meta = KINDS.find((k) => k.value === kind)!;
  const st = c.status;

  return (
    <>
      <PageHeader title="Alerts" subtitle={`Get a message when a problem starts or is resolved in ${clusterId}. Checked every ${goDuration(c.interval)}.`} />
      <div className="grid gap-4 lg:grid-cols-5">
        <Card title="Where to send alerts" className="lg:col-span-3">
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <label className="block">
              <span className="mb-1 block text-sm font-medium">Destination</span>
              <select className="w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm" value={kind} onChange={(e) => setKind(e.target.value as AlertKind)}>
                {KINDS.map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </select>
            </label>
            <label className="block">
              <span className="mb-1 block text-sm font-medium">Webhook URL</span>
              <input
                className="w-full rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm"
                type="url"
                autoComplete="off"
                placeholder={c.configured ? `Saved: ${c.url} — leave empty to keep it` : meta.placeholder}
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
              <span className="mt-1 block text-xs text-muted">{meta.help} The URL is a secret: it's stored on the server and only shown masked.</span>
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="size-4" checked={on} onChange={(e) => setOn(e.target.checked)} />
              Alerts enabled
            </label>
            {save.isError && <ErrorBox error={save.error} />}
            <div className="flex flex-wrap gap-2">
              <Button type="submit" variant="primary" busy={save.isPending} disabled={!c.configured && !url.trim()}>
                Save
              </Button>
              {c.configured && (
                <>
                  <Button type="button" busy={test.isPending} onClick={() => test.mutate()}>
                    <Send className="size-4" /> Send test message
                  </Button>
                  <Button
                    type="button"
                    variant="danger"
                    busy={remove.isPending}
                    onClick={() => {
                      if (confirm("Remove alerts for this cluster?")) remove.mutate();
                    }}
                  >
                    <Trash2 className="size-4" /> Remove
                  </Button>
                </>
              )}
            </div>
            {test.data && (test.data.ok ? <Badge tone="ok">Test message delivered</Badge> : <ErrorBox error={new Error(test.data.error ?? "Delivery failed")} />)}
          </form>
        </Card>

        <div className="space-y-4 lg:col-span-2">
          <Card
            title={
              <span className="flex items-center gap-2">
                <Bell className="size-4" /> Status
              </span>
            }
          >
            {!c.configured ? (
              <p className="text-sm text-muted">Not set up yet.</p>
            ) : (
              <dl className="space-y-1.5 text-sm">
                <div className="flex justify-between gap-2">
                  <dt className="text-muted">Alerts</dt>
                  <dd>{c.enabled ? <Badge tone="ok">on</Badge> : <Badge>off</Badge>}</dd>
                </div>
                <div className="flex justify-between gap-2">
                  <dt className="text-muted">Last check</dt>
                  <dd>{st.lastScanAt ? `${age(st.lastScanAt)} ago` : c.enabled ? "within a minute" : "—"}</dd>
                </div>
                <div className="flex justify-between gap-2">
                  <dt className="text-muted">Last message sent</dt>
                  <dd>{st.lastNotifiedAt ? `${age(st.lastNotifiedAt)} ago` : "none yet"}</dd>
                </div>
              </dl>
            )}
            {st.lastScanError && (
              <p className="mt-3 flex items-start gap-1.5 text-xs text-warn">
                <CircleAlert className="mt-px size-3.5 shrink-0" /> Can't check right now: {st.lastScanError}
              </p>
            )}
            {st.lastDeliveryError && (
              <p className="mt-2 flex items-start gap-1.5 text-xs text-bad">
                <CircleAlert className="mt-px size-3.5 shrink-0" /> Last delivery failed: {st.lastDeliveryError}
              </p>
            )}
          </Card>

          <Card title={`Problems being tracked (${st.active.length})`} bodyClassName="p-0">
            {st.active.length === 0 ? (
              <div className="flex items-center gap-2 px-4 py-3 text-sm text-muted">
                <CircleCheck className="size-4 text-ok" /> {st.lastScanAt ? "Nothing wrong right now." : "Waiting for the first check."}
              </div>
            ) : (
              <ul className="divide-y divide-line">
                {st.active.map((a) => (
                  <li key={a.key} className="space-y-1 px-4 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={failureTone(a.failureType)}>{failureLabel(a.failureType)}</Badge>
                      <span className="text-xs text-muted">since {age(a.since)} ago</span>
                    </div>
                    <div className="text-sm">{a.title}</div>
                    <div className="font-mono text-[11.5px] break-all text-muted">
                      {a.pods.slice(0, 3).join(", ")}
                      {a.pods.length > 3 ? ` +${a.pods.length - 3} more` : ""}
                    </div>
                  </li>
                ))}
              </ul>
            )}
            {st.baseline && st.active.length > 0 && (
              <p className="border-t border-line px-4 py-2 text-xs text-muted">
                Problems that already existed when alerts were turned on are tracked without a message; you'll hear when they're resolved and about anything new.
              </p>
            )}
          </Card>
        </div>
      </div>
    </>
  );
}
