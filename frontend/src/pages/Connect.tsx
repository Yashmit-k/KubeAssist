import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Download, Loader2, LogIn, PlugZap, ShieldCheck, Trash2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { useHealth } from "../components/Layout";
import SignIn, { authErrorMessage } from "../components/SignIn";
import { Badge, Button, buttonClass, Card, Command, CopyButton, Dot, Empty, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { api } from "../lib/api";
import { installCommand } from "../lib/commands";
import { useMe, useMyClusters } from "../lib/clusters";
import { age } from "../lib/format";
import type { ClusterStatus, RegisteredCluster } from "../lib/types";

const ID_RE = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

function download(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/yaml" }));
  const a = Object.assign(document.createElement("a"), { href: url, download: filename });
  a.click();
  URL.revokeObjectURL(url);
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li>
      <div className="mb-1.5 text-sm font-medium">
        {n}. {title}
      </div>
      {children}
    </li>
  );
}

function Hint({ title, children }: { title: string; children: ReactNode }) {
  return (
    <details className="mt-2 rounded-lg border border-line px-3 py-2">
      <summary className="cursor-pointer text-xs font-medium text-muted">{title}</summary>
      <div className="mt-2 space-y-2 text-xs text-muted">{children}</div>
    </details>
  );
}

/** How long to wait for the agent before showing troubleshooting help. */
const SLOW_CONNECT_MS = 60_000;

function Registered({ reg }: { reg: RegisteredCluster }) {
  const status = useQuery({
    queryKey: ["remote-status", reg.clusterId],
    queryFn: () => api.remoteStatus(reg.clusterId),
    refetchInterval: 3000,
    retry: false,
  });
  const connected = status.data?.connected;
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setSlow(true), SLOW_CONNECT_MS);
    return () => clearTimeout(t);
  }, []);
  const file = `kubeassist-agent-${reg.clusterId}.yaml`;
  const install = installCommand(reg.manifest);
  const lines = reg.manifest.trimEnd().split("\n").length;

  return (
    <Card title={`Install the agent in “${reg.clusterId}”`}>
      <ol className="space-y-5">
        <Step n={1} title="Check that kubectl reaches the right cluster">
          <p className="mb-2 text-sm text-muted">In the terminal you'll install from, confirm which cluster is selected and that it answers:</p>
          <div className="space-y-1.5">
            <Command cmd="kubectl config current-context" />
            <Command cmd="kubectl get nodes" />
          </div>
          <Hint title="kubectl get nodes fails or shows the wrong cluster?">
            <p>
              <b className="text-fg">Wrong cluster:</b> list them with <code className="font-mono">kubectl config get-contexts</code> and switch with{" "}
              <code className="font-mono">kubectl config use-context &lt;name&gt;</code>.
            </p>
            <p>
              <b className="text-fg">“connection refused” on a local cluster (Kind, minikube, Docker Desktop):</b> the cluster runs inside Docker, so start
              Docker Desktop first and wait until it says it's running. Then check the cluster: <code className="font-mono">kind get clusters</code> or{" "}
              <code className="font-mono">minikube status</code> (<code className="font-mono">minikube start</code> if it's stopped).
            </p>
            <p>
              <b className="text-fg">Cloud cluster (“Unauthorized” or expired credentials):</b> refresh them, e.g.{" "}
              <code className="font-mono">aws eks update-kubeconfig --name &lt;cluster&gt;</code>,{" "}
              <code className="font-mono">gcloud container clusters get-credentials &lt;cluster&gt;</code> or{" "}
              <code className="font-mono">az aks get-credentials -n &lt;cluster&gt; -g &lt;group&gt;</code>.
            </p>
            <p>
              <b className="text-fg">Permissions:</b> installing creates a ClusterRole, so <code className="font-mono">kubectl auth can-i create clusterrole</code> must
              answer <code className="font-mono">yes</code>.
            </p>
          </Hint>
        </Step>

        <Step n={2} title="Install the agent — paste this one command">
          <p className="mb-2 text-sm text-muted">
            Copy it and paste it into the same terminal (macOS, Linux or WSL). It applies the manifest directly — no file to save. Read-only RBAC (no Secrets,
            no writes); the agent connects outbound to <code className="font-mono text-xs">{reg.relayUrl}</code>, so nothing in your cluster is exposed.
          </p>
          <div className="rounded-lg bg-code-bg px-3 py-2.5 font-mono text-[12.5px] text-code-fg ring-1 ring-warn/60">
            <div className="flex items-start gap-2">
              <span className="select-none text-faint">$</span>
              <div className="min-w-0 flex-1">
                <div>{install.split("\n")[0]}</div>
                <div className="text-faint">… agent manifest ({lines} lines) …</div>
                <div>{install.trimEnd().split("\n").at(-1)}</div>
              </div>
            </div>
            <div className="mt-2 flex flex-wrap gap-2">
              <CopyButton text={install} label="Copy install command" className="bg-white/10 px-2.5 py-1.5 text-code-fg hover:bg-white/20 hover:text-code-fg" />
            </div>
          </div>
          <p className="mt-2 text-xs text-muted">
            The command contains the agent's token and is shown only now — install it (or download the file) before leaving this page.
          </p>
          <Hint title="Windows PowerShell, or prefer a file?">
            <div className="flex flex-wrap items-center gap-2">
              <Button onClick={() => download(file, reg.manifest)}>
                <Download className="size-4" /> Download {file}
              </Button>
              <span>then run it from the folder you saved it in:</span>
            </div>
            <Command cmd={`kubectl apply -f ${file}`} write />
            <pre className="max-h-60 overflow-auto rounded-lg bg-code-bg p-3 font-mono text-[11px] text-code-fg">{reg.manifest}</pre>
          </Hint>
        </Step>

        <Step n={3} title="Wait for it to connect">
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
            <>
              <div className="flex items-center gap-2 text-sm text-muted">
                <Loader2 className="size-4 animate-spin" /> Waiting for the agent… (usually under 30 seconds)
              </div>
              {slow && (
                <div className="mt-3 space-y-1.5 rounded-lg border border-warn/40 bg-warn-soft px-3 py-2.5 text-sm">
                  <p>Still not connected? Check the agent's pod and logs:</p>
                  <Command cmd="kubectl -n kubeassist get pods" />
                  <Command cmd="kubectl -n kubeassist logs deployment/kubeassist-agent --tail=20" />
                  <p className="text-xs text-muted">
                    <b>ImagePullBackOff</b>: the cluster can't download the agent image. <b>401 / unauthorized</b> in the logs: the cluster was registered
                    again, which issues a new token — paste the newest install command. <b>No pod at all</b>: the install command didn't run; check step 1.
                  </p>
                </div>
              )}
            </>
          )}
        </Step>
      </ol>
    </Card>
  );
}

function ClusterRow({ c }: { c: ClusterStatus }) {
  const qc = useQueryClient();
  const revoke = useMutation({
    mutationFn: () => api.revokeCluster(c.clusterId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["clusters"] });
      qc.removeQueries({ queryKey: ["remote-status", c.clusterId] });
    },
  });
  const tone = c.connected ? "ok" : "warn";
  const label = c.connected ? `connected ${age(c.connectedAt)}` : "agent offline";

  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3">
      <Dot tone={tone} />
      <Link to={`/c/${encodeURIComponent(c.clusterId)}/pods`} className="font-medium hover:text-accent">
        {c.clusterId}
      </Link>
      <span className="text-xs text-muted">{label}</span>
      <span className="flex-1" />
      <Button
        variant="danger"
        className="text-xs"
        busy={revoke.isPending}
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

export default function Connect() {
  const health = useHealth();
  const me = useMe();
  const mine = useMyClusters();
  const qc = useQueryClient();
  const [params] = useSearchParams();
  const authError = authErrorMessage(params.get("authError"));
  const [clusterId, setClusterId] = useState("");
  const [reg, setReg] = useState<RegisteredCluster | null>(null);
  const register = useMutation({
    mutationFn: (id: string) => api.registerCluster(id),
    onSuccess: (r) => {
      setReg(r);
      void qc.invalidateQueries({ queryKey: ["clusters"] });
    },
  });

  if (health.isLoading || me.isLoading) return <Spinner />;
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
      ) : !me.data ? (
        <Card title={<span className="flex items-center gap-2"><LogIn className="size-4" /> Sign in to connect a cluster</span>} className="max-w-lg">
          <p className="mb-3 text-sm text-muted">Clusters belong to your account, so you can open them from any browser. Nobody else can see or query them.</p>
          {authError && <div className="mb-3"><ErrorBox error={new Error(authError)} /></div>}
          {me.isError && <div className="mb-3"><ErrorBox error={me.error} title="Could not check your session" /></div>}
          <SignIn next="/connect" />
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
          </div>

          <div className="space-y-4 lg:col-span-2">
            <Card title="Your clusters" bodyClassName="p-0">
              {mine.isLoading ? (
                <Spinner />
              ) : mine.isError ? (
                <div className="p-4"><ErrorBox error={mine.error} /></div>
              ) : !mine.data?.length ? (
                <Empty title="None yet" />
              ) : (
                <ul className="divide-y divide-line">
                  {mine.data.map((c) => (
                    <ClusterRow key={c.clusterId} c={c} />
                  ))}
                </ul>
              )}
            </Card>
            <Card title={<span className="flex items-center gap-2"><ShieldCheck className="size-4" /> How it stays safe</span>}>
              <ul className="space-y-2 text-sm text-muted">
                <li>The agent dials out over a WebSocket — no inbound access to your cluster.</li>
                <li>Its RBAC is get/list/watch only and excludes Secrets.</li>
                <li>Logs and messages are redacted inside your cluster before they leave it.</li>
                <li>Only your signed-in account can query your clusters; the agent's token authenticates the agent alone.</li>
                <li>Suggested commands are never executed — you run them.</li>
              </ul>
            </Card>
          </div>
        </div>
      )}
    </>
  );
}
