import { Loader2 } from "lucide-react";
import { Link } from "react-router";

/** Shown instead of an error while a remote cluster's agent isn't
 * connected; the page's useAgentWait refetches once it connects. */
export default function AgentWaiting({ clusterId }: { clusterId: string }) {
  return (
    <div role="status" className="flex gap-2 rounded-lg border border-warn/40 bg-warn-soft px-3 py-2.5 text-sm">
      <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-warn" />
      <div>
        <div className="font-medium">Waiting for the agent</div>
        <p className="text-muted">
          The agent for <span className="font-medium text-fg">{clusterId}</span> isn't connected yet. Install it from{" "}
          <Link to="/connect" className="text-accent hover:underline">
            Connect cluster
          </Link>
          , or check <code className="rounded bg-surface-2 px-1 font-mono text-[12px]">kubectl -n kubeassist logs deploy/kubeassist-agent</code>. This page
          refreshes automatically.
        </p>
      </div>
    </div>
  );
}
