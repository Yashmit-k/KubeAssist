import { GitCommitHorizontal, History } from "lucide-react";
import { age } from "../lib/format";
import type { Rollout, TemplateChange } from "../lib/types";
import { Badge, Card, Command } from "./ui";

function changeLabel(c: TemplateChange): string {
  const what = c.name ? `${c.field} ${c.name}` : c.field;
  return c.container ? `${what} (${c.container})` : what;
}

/** "What changed?" — the pod's Deployment revision and the diff from
 * the previous revision, with a rollback command. Read-only. */
export default function RolloutCard({ r, namespace, failing }: { r: Rollout; namespace: string; failing: boolean }) {
  const hasPrev = !!r.previousRevision;
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <History className="size-4" /> What changed
        </span>
      }
      actions={<span className="font-mono text-xs text-muted">{r.workload}</span>}
    >
      <ol className="relative space-y-4 border-l border-line pl-5">
        <li>
          <span className="absolute -left-[7px] mt-1 size-3 rounded-full bg-accent" />
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="font-medium">Revision {r.revision}</span>
            <span className="text-muted">rolled out {age(r.rolledOutAt)} ago</span>
            {r.latest ? <Badge tone="info">current</Badge> : <Badge tone="warn">older than the Deployment's newest revision (rollout in progress?)</Badge>}
            <span className="text-xs text-muted">· this pod runs it</span>
          </div>
          {hasPrev && r.changes.length > 0 && (
            <ul className="mt-2 space-y-1.5">
              {r.changes.map((c, i) => (
                <li key={i} className="text-sm">
                  <span className="flex items-start gap-1.5">
                    <GitCommitHorizontal className="mt-0.5 size-3.5 shrink-0 text-faint" />
                    <span className="min-w-0">
                      <span className="font-medium">{changeLabel(c)}</span>{" "}
                      {c.from && <code className="rounded bg-bad-soft px-1 font-mono text-[12px] break-all line-through decoration-bad/60">{c.from}</code>}
                      {c.from && c.to && <span className="mx-1 text-faint">→</span>}
                      {c.to && <code className="rounded bg-ok-soft px-1 font-mono text-[12px] break-all">{c.to}</code>}
                    </span>
                  </span>
                </li>
              ))}
            </ul>
          )}
          {hasPrev && r.changes.length === 0 && <p className="mt-1 text-sm text-muted">No pod-template change from the previous revision (e.g. a restart).</p>}
        </li>
        {hasPrev ? (
          <li>
            <span className="absolute -left-[7px] mt-1 size-3 rounded-full border-2 border-line bg-surface" />
            <div className="text-sm">
              <span className="font-medium">Revision {r.previousRevision}</span> <span className="text-muted">— the version before</span>
            </div>
          </li>
        ) : (
          <li className="text-sm text-muted">This is the Deployment's first revision — nothing to compare with.</li>
        )}
      </ol>
      {hasPrev && failing && r.latest && r.changes.length > 0 && (
        <div className="mt-4 space-y-1.5 border-t border-line pt-3">
          <p className="text-sm">If this rollout caused the failure, roll back to the version that ran before:</p>
          <Command cmd={`kubectl rollout undo ${r.workload} -n ${namespace} --to-revision=${r.previousRevision}`} write />
        </div>
      )}
    </Card>
  );
}
