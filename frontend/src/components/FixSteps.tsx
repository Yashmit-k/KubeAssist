import { PencilLine, ShieldAlert } from "lucide-react";
import { hasPlaceholder } from "../lib/commands";
import { Badge, Command } from "./ui";

export type Step = { description: string; command?: string | null; isWriteAction?: boolean };

/** Numbered fix steps. Nothing here runs anything: commands are
 * copy-only, cluster-changing ones are flagged, and any value the
 * evidence couldn't know is highlighted for the user to fill in. */
export default function FixSteps({ steps }: { steps: Step[] }) {
  return (
    <ol className="space-y-3">
      {steps.map((s, i) => (
        <li key={i} className="flex gap-3">
          <span className="grid size-6 shrink-0 place-items-center rounded-full bg-surface-2 text-xs font-semibold text-muted">{i + 1}</span>
          <div className="min-w-0 flex-1 space-y-1.5">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span>{s.description}</span>
              {s.isWriteAction && (
                <Badge tone="warn" title="This command changes your cluster. Review it before running.">
                  <ShieldAlert className="size-3" /> changes cluster
                </Badge>
              )}
              {s.command && hasPlaceholder(s.command) && (
                <Badge tone="info" title="The highlighted value isn't in the evidence — only you know it. Replace it before running.">
                  <PencilLine className="size-3" /> fill in highlighted value
                </Badge>
              )}
            </div>
            {s.command && <Command cmd={s.command} write={s.isWriteAction} />}
          </div>
        </li>
      ))}
    </ol>
  );
}
