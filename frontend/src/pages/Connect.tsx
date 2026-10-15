import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Download, KeyRound, Loader2, PlugZap, ShieldCheck, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { useHealth } from "../components/Layout";
import { Badge, Button, buttonClass, Card, Command, CopyButton, Dot, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { api, ApiError } from "../lib/api";
import { savedClusters, useSavedClusters, type SavedCluster } from "../lib/clusters";
import { age } from "../lib/format";
import type { RegisteredCluster } from "../lib/types";

const ID_RE = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

function useStatus(c: SavedCluster, poll: number | false) {
  return useQuery({
    queryKey: ["remote-status", c.clusterId],
    queryFn: () => api.remoteStatus({ kind: "remote", clusterId: c.clusterId, token: c.token }),
    refetchInterval: poll,
    retry: false,
  });
}

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
  const a = Object.assign(document.createElement("a"), { href: url, download: filename });
  a.click();
  URL.revokeObjectURL(url);
}

function Registered({ reg }: { reg: RegisteredCluster }) {
  const saved = { clusterId: reg.clusterId, token: reg.token, addedAt: "" };
  const status = useStatus(saved, 3000);
  const connected = status.data?.connected;
  const file = `kubeassist-agent-${reg.clusterId}.yaml`;

  return (
    <Card title={`Install the agent in “${reg.clusterId}”`}>
      <ol className="space-y-5">
        <li>
          <div className="mb-1.5 text-sm font-medium">1. Keep your access token</div>
          <p className="mb-2 text-sm text-muted">
            It's saved in this browser and shown only once — the server keeps just a hash. You need it to open this cluster from another browser.
          </p>
          <div className="flex items-center gap-2 rounded-lg border border-line bg-surface-2 px-3 py-2">
            <KeyRound className="size-4 shrink-0 text-faint" />
            <code className="min-w-0 flex-1 truncate font-mono text-xs">{reg.token}</code>
            <CopyButton text={reg.token} label="Copy" />
          </div>
        </li>
        <li>
          <div className="mb-1.5 text-sm font-medium">2. Apply the agent manifest</div>
          <p className="mb-2 text-sm text-muted">
            Read-only RBAC (no Secrets, no writes). The agent connects outbound to <code className="font-mono text-xs">{reg.relayUrl}</code>; nothing in your cluster is exposed.
          </p>
          <div className="mb-2 flex flex-wrap gap-2">
            <Button onClick={() => download(file, reg.manifest)}>
              <Download className="size-4" /> Download {file}
            </Button>
            <CopyButton text={reg.manifest} label="Copy manifest" className="border border-line px-2.5" />
          </div>
          <Command cmd={`kubectl apply -f ${file}`} write />
          <details className="mt-2">
            <summary className="cursor-pointer text-xs text-muted">View manifest</summary>
            <pre className="mt-2 max-h-80 overflow-auto rounded-lg bg-code-bg p-3 font-mono text-[11px] text-code-fg">{reg.manifest}</pre>
          </details>
        </li>
        <li>
          <div className="mb-1.5 text-sm font-medium">3. Wait for it to connect</div>
          {connected ? (
            <div className="flex flex-wrap items-center gap-3">
              <Badge tone="ok">
                <CheckCircle2 className="size-3.5" /> Agent connected
              </Badge>
              <Link to={`/c/${encodeURIComponent(reg.clusterId)}/pods`} className={buttonClass("primary")}>
                Open cluster
              </Link>
            </div>
          ) : (
            <div className="flex items-center gap-2 text-sm text-muted">
              <Loader2 className="size-4 animate-spin" /> Waiting for the agent…
            </div>
          )}
        </li>
      </ol>
    </Card>
  );
}

function SavedRow({ c }: { c: SavedCluster }) {
  const status = useStatus(c, 15_000);
  const qc = useQueryClient();
  const revoke = useMutation({
    mutationFn: () => api.revokeCluster({ kind: "remote", clusterId: c.clusterId, token: c.token }),
    onSuccess: () => {
      savedClusters.remove(c.clusterId);
      void qc.invalidateQueries({ queryKey: ["remote-status", c.clusterId] });
    },
  });
  const invalid = status.error instanceof ApiError && status.error.status === 401;
  const tone = invalid ? "bad" : status.data?.connected ? "ok" : status.isLoading ? "muted" : "warn";
  const label = invalid ? "token rejected" : status.data?.connected ? `connected ${age(status.data.connectedAt)}` : status.isLoading ? "checking…" : "agent offline";

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <Dot tone={tone} />
      <Link to={`/c/${encodeURIComponent(c.clusterId)}/pods`} className="font-medium hover:text-accent">
        {c.clusterId}
      </Link>
      <span className="text-xs text-muted">{label}</span>
      <span className="flex-1" />
      <Button variant="ghost" className="text-xs" onClick={() => savedClusters.remove(c.clusterId)} title="Forget the token in this browser (the cluster stays registered)">
        Forget
      </Button>
      <Button
        variant="danger"
        className="text-xs"
        busy={revoke.isPending}
        disabled={invalid}
        onClick={() => {
          if (confirm(`Revoke "${c.clusterId}"? The agent is disconnected, its token stops working and its history is deleted.`)) revoke.mutate();
        }}
      >
        <Trash2 className="size-3.5" /> Revoke
      </Button>
      {revoke.isError && <div className="w-full"><ErrorBox error={revoke.error} /></div>}
    </li>
  );
}

function AddExisting() {
  const [id, setId] = useState("");
  const [token, setToken] = useState("");
  const add = useMutation({
    mutationFn: async () => {
      await api.remoteStatus({ kind: "remote", clusterId: id.trim(), token: token.trim() });
      savedClusters.add(id.trim(), token.trim());
    },
  });
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate();
      }}
    >
      <div className="flex flex-wrap gap-2">
        <input className="min-w-40 flex-1 rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm" placeholder="cluster id" value={id} onChange={(e) => setId(e.target.value)} />
        <input
          className="min-w-56 flex-[2] rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm"
          type="password"
          autoComplete="off"
          placeholder="token"
          value={token}
          onChange={(e) => setToken(e.target.value)}
        />
        <Button type="submit" busy={add.isPending} disabled={!id.trim() || !token.trim()}>
          Add
        </Button>
      </div>
      {add.isError && <ErrorBox error={add.error instanceof ApiError && add.error.status === 401 ? new Error("That token is not valid for this cluster.") : add.error} />}
      {add.isSuccess && <p className="text-xs text-ok">Added.</p>}
    </form>
  );
}

export default function Connect() {
  const health = useHealth();
  const saved = useSavedClusters();
  const [clusterId, setClusterId] = useState("");
  const [reg, setReg] = useState<RegisteredCluster | null>(null);
  const register = useMutation({
    mutationFn: (id: string) => api.registerCluster(id),
    onSuccess: (r) => {
      savedClusters.add(r.clusterId, r.token);
      setReg(r);
    },
  });

  if (health.isLoading) return <Spinner />;
  const relay = health.data?.relay;
  const valid = ID_RE.test(clusterId);

  return (
    <>
      <PageHeader title="Connect a cluster" subtitle="Diagnose pods in any cluster by running a small read-only agent inside it." />

      {!relay ? (
        <Card>
          <Empty icon={<PlugZap className="size-8" />} title="Remote clusters are disabled on this backend">
            Start the Go backend with <code className="font-mono">ENABLE_RELAY=true</code> (and <code className="font-mono">RELAY_PUBLIC_URL</code>) to connect clusters through the agent.
          </Empty>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-5">
          <div className="space-y-4 lg:col-span-3">
            {reg ? (
              <>
                <Registered reg={reg} />
                <Button variant="ghost" onClick={() => { setReg(null); setClusterId(""); register.reset(); }}>
                  Register another cluster
                </Button>
              </>
            ) : (
              <Card title="Register a new cluster">
                <form
                  className="space-y-3"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (valid) register.mutate(clusterId);
                  }}
                >
                  <label className="block">
                    <span className="mb-1 block text-sm font-medium">Cluster ID</span>
                    <input
                      className="w-full rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm"
                      placeholder="prod-eu-1"
                      value={clusterId}
                      onChange={(e) => setClusterId(e.target.value.toLowerCase())}
                    />
                    <span className="mt-1 block text-xs text-muted">Lowercase letters, digits and dashes.</span>
                  </label>
                  {register.isError && <ErrorBox error={register.error} />}
                  <Button type="submit" variant="primary" busy={register.isPending} disabled={!valid}>
                    Register & get agent manifest
                  </Button>
                </form>
              </Card>
            )}
            <Card title="Already registered elsewhere?">
              <p className="mb-3 text-sm text-muted">Add a cluster to this browser with its ID and token.</p>
              <AddExisting />
            </Card>
          </div>

          <div className="space-y-4 lg:col-span-2">
            <Card title="Your clusters (this browser)" bodyClassName="p-0">
              {saved.length === 0 ? (
                <Empty title="None yet" />
              ) : (
                <ul className="divide-y divide-line">
                  {saved.map((c) => (
                    <SavedRow key={c.clusterId} c={c} />
                  ))}
                </ul>
              )}
            </Card>
            <Card title={<span className="flex items-center gap-2"><ShieldCheck className="size-4" /> How it stays safe</span>}>
              <ul className="space-y-2 text-sm text-muted">
                <li>The agent dials out over a WebSocket — no inbound access to your cluster.</li>
                <li>Its RBAC is get/list/watch only and excludes Secrets.</li>
                <li>Logs and messages are redacted inside your cluster before they leave it.</li>
                <li>Every API call for a cluster requires that cluster's token.</li>
                <li>Suggested commands are never executed — you run them.</li>
              </ul>
            </Card>
          </div>
        </div>
      )}
    </>
  );
}
