import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, GitPullRequest, Lock, Unplug } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useLocation, useSearchParams } from "react-router";
import { api } from "../lib/api";
import type { FixOption } from "../lib/types";
import { Badge, Button, buttonClass, ErrorBox, Spinner } from "./ui";

const REPO_ERRORS: Record<string, string> = {
  account_mismatch: "The GitHub account you approved isn't the one you signed in with. Approve with the same account.",
  github_account_required: "Repository access needs a GitHub sign-in (not the development sign-in).",
  access_denied: "GitHub access was cancelled.",
};

function Section({ children }: { children: ReactNode }) {
  return <section className="rounded-xl border border-line bg-surface px-5 py-4">{children}</section>;
}

function Header() {
  return (
    <div className="mb-3">
      <h3 className="flex items-center gap-2 text-sm font-semibold">
        <GitPullRequest className="size-4" /> Fix it in Git
      </h3>
      <p className="text-xs text-muted">
        Open a pull request that changes the manifest in your repository, so the fix survives the next deploy. You review and merge it; nothing is applied to
        the cluster.
      </p>
    </div>
  );
}

function OptionForm({ o, clusterId, ns, name }: { o: FixOption; clusterId: string; ns: string; name: string }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const open = useMutation({ mutationFn: () => api.openFixPR(clusterId, ns, name, o.id, values) });
  return (
    <li className="space-y-2 rounded-lg border border-line px-4 py-3">
      <div className="text-sm font-medium">{o.title}</div>
      <p className="text-xs text-muted">{o.description}</p>
      {o.unavailable ? (
        <p className="text-xs text-warn">{o.unavailable}</p>
      ) : open.data ? (
        <div className="space-y-1.5">
          <a href={open.data.url} target="_blank" rel="noreferrer" className={buttonClass("primary")}>
            <ExternalLink className="size-4" /> Pull request #{open.data.number}
          </a>
          <p className="text-xs text-muted">
            Changed <code className="font-mono">{open.data.file}</code> on branch <code className="font-mono">{open.data.branch}</code>:{" "}
            {open.data.changes.join("; ")}
          </p>
        </div>
      ) : (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            open.mutate();
          }}
        >
          {o.inputs.map((i) => (
            <label key={i.name} className="block">
              <span className="mb-0.5 block text-xs font-medium">{i.label}</span>
              <input
                className="w-full rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm"
                placeholder={i.default || i.placeholder}
                value={values[i.name] ?? ""}
                onChange={(e) => setValues((v) => ({ ...v, [i.name]: e.target.value }))}
              />
              {i.default && <span className="text-[11px] text-faint">Leave empty for {i.default}</span>}
            </label>
          ))}
          {open.isError && <ErrorBox error={open.error} />}
          <Button type="submit" variant="primary" busy={open.isPending}>
            <GitPullRequest className="size-4" /> Open pull request
          </Button>
        </form>
      )}
    </li>
  );
}

function LinkForm({ clusterId, onSaved }: { clusterId: string; onSaved: () => void }) {
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("");
  const [path, setPath] = useState("");
  const save = useMutation({ mutationFn: () => api.saveRepoLink(clusterId, { repo, branch: branch || undefined, path }), onSuccess: onSaved });
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <p className="text-sm">Which repository holds this cluster's manifests?</p>
      <div className="grid gap-2 sm:grid-cols-3">
        <input className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm sm:col-span-3" placeholder="owner/repository" value={repo} onChange={(e) => setRepo(e.target.value)} />
        <input className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm" placeholder="branch (default)" value={branch} onChange={(e) => setBranch(e.target.value)} />
        <input className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm sm:col-span-2" placeholder="folder, e.g. deploy/k8s (whole repo if empty)" value={path} onChange={(e) => setPath(e.target.value)} />
      </div>
      {save.isError && <ErrorBox error={save.error} />}
      <Button type="submit" busy={save.isPending} disabled={!repo.trim()}>
        Link repository
      </Button>
    </form>
  );
}

/** Pull-request fixes for a pod in a connected cluster. */
export default function FixPRPanel({ clusterId, ns, name }: { clusterId: string; ns: string; name: string }) {
  const qc = useQueryClient();
  const location = useLocation();
  const [params] = useSearchParams();
  const access = useQuery({ queryKey: ["repo-access"], queryFn: api.repoAccess, retry: false });
  const options = useQuery({
    queryKey: ["fix-options", clusterId, ns, name],
    queryFn: () => api.fixOptions(clusterId, ns, name),
    enabled: !!access.data?.connected,
  });
  const disconnect = useMutation({ mutationFn: api.disconnectRepoAccess, onSuccess: () => void qc.invalidateQueries({ queryKey: ["repo-access"] }) });
  const unlink = useMutation({ mutationFn: () => api.deleteRepoLink(clusterId), onSuccess: () => void qc.invalidateQueries({ queryKey: ["fix-options"] }) });

  const back = location.pathname; // return here (without ?run=1) after GitHub
  const repoError = params.get("repoAccessError");

  if (access.isLoading) return null;
  if (access.isError || !access.data?.available) return null; // feature off on this backend

  const a = access.data;
  if (!a.connected) {
    return (
      <Section>
        <Header />
        {repoError && <ErrorBox error={new Error(REPO_ERRORS[repoError] ?? `GitHub access failed (${repoError}).`)} />}
        {!a.githubAccount ? (
          <p className="text-sm text-muted">Sign in with GitHub to connect repositories.</p>
        ) : (
          <div className="space-y-2">
            <p className="text-sm">Connect GitHub repository access. This is separate from sign-in, and you can disconnect it at any time.</p>
            <div className="flex flex-wrap gap-2">
              <a href={api.repoAccessStartUrl(false, back)} className={buttonClass("primary")}>
                <GitPullRequest className="size-4" /> Connect public repositories
              </a>
              <a href={api.repoAccessStartUrl(true, back)} className={buttonClass("secondary")}>
                <Lock className="size-4" /> Include private repositories
              </a>
            </div>
            <p className="text-[11px] text-faint">GitHub will ask to grant write access to repositories, which is needed to push a branch and open the pull request.</p>
          </div>
        )}
      </Section>
    );
  }

  return (
    <Section>
      <Header />
      <div className="mb-3 flex flex-wrap items-center gap-2 text-xs text-muted">
        <Badge tone="ok">GitHub connected{a.private ? " (incl. private)" : " (public repos)"}</Badge>
        {options.data?.link && (
          <span>
            Repository <code className="font-mono">{options.data.link.repo}</code> @ <code className="font-mono">{options.data.link.branch}</code>
            {options.data.link.path && (
              <>
                {" "}
                / <code className="font-mono">{options.data.link.path}</code>
              </>
            )}{" "}
            <button type="button" className="underline hover:text-fg" onClick={() => unlink.mutate()}>
              change
            </button>
          </span>
        )}
        <span className="flex-1" />
        <button type="button" className="flex items-center gap-1 hover:text-fg" onClick={() => disconnect.mutate()} title="Remove KubeAssist's access to your repositories">
          <Unplug className="size-3.5" /> Disconnect GitHub
        </button>
      </div>
      {options.isLoading ? (
        <Spinner />
      ) : options.isError ? (
        <ErrorBox error={options.error} />
      ) : !options.data!.link ? (
        <LinkForm clusterId={clusterId} onSaved={() => void qc.invalidateQueries({ queryKey: ["fix-options"] })} />
      ) : options.data!.options.length === 0 ? (
        <p className="text-sm text-muted">No fix for this diagnosis can be written as an exact manifest change; use the steps above.</p>
      ) : (
        <ul className="space-y-2">
          {options.data!.options.map((o) => (
            <OptionForm key={o.id} o={o} clusterId={clusterId} ns={ns} name={name} />
          ))}
        </ul>
      )}
    </Section>
  );
}
