import { AlertTriangle, BookOpen, CheckCircle2, CircleHelp, ExternalLink, PencilLine, RotateCcw, ShieldAlert, Wrench } from "lucide-react";
import type { ReactNode } from "react";
import { hasPlaceholder } from "../lib/commands";
import { failureLabel, failureTone, modeLabel } from "../lib/format";
import type { Diagnosis } from "../lib/types";
import { Badge, Command, Confidence } from "./ui";

function Section({ icon, title, children }: { icon: ReactNode; title: string; children: ReactNode }) {
  return (
    <section className="border-t border-line px-5 py-4">
      <h3 className="mb-2.5 flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-muted">
        {icon}
        {title}
      </h3>
      {children}
    </section>
  );
}

/** Renders a DiagnosisResponse. Nothing here executes anything: every
 * command is copy-only, and cluster-changing ones are flagged. */
export default function DiagnosisView({ d, aiError }: { d: Diagnosis; aiError?: string }) {
  const mode = modeLabel(d.mode);
  const steps = [...(d.resolutionSteps ?? [])].sort((a, b) => a.order - b.order);

  return (
    <article className="overflow-hidden rounded-xl border border-line bg-surface">
      <header className="px-5 py-4">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <Badge tone={failureTone(d.failureType)} className="text-sm">
            {failureLabel(d.failureType)}
          </Badge>
          <code className="text-xs text-faint">{d.failureType}</code>
          <span className="flex-1" />
          <Badge tone="info" title={mode.hint}>
            {mode.label}
          </Badge>
          <Confidence value={d.confidenceScore} />
        </div>
        <p className="text-[15px] leading-relaxed">{d.issueSummary}</p>
        {aiError && (
          <p className="mt-2 flex items-start gap-1.5 text-xs text-warn">
            <AlertTriangle className="mt-px size-3.5 shrink-0" />
            AI service unavailable — showing the detector-only answer. ({aiError})
          </p>
        )}
      </header>

      <Section icon={<CheckCircle2 className="size-3.5" />} title="Evidence">
        <ul className="space-y-1.5">
          {d.observedEvidence.map((e, i) => (
            <li key={i} className="flex gap-2 text-sm">
              <span className="mt-2 size-1 shrink-0 rounded-full bg-faint" />
              <span className="font-mono text-[12.5px] leading-relaxed break-words">{e}</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section icon={<CircleHelp className="size-3.5" />} title="Probable root cause">
        <p className="text-sm leading-relaxed">{d.probableRootCause}</p>
        {d.reasoning && <p className="mt-2 text-sm leading-relaxed text-muted">{d.reasoning}</p>}
      </Section>

      <Section icon={<Wrench className="size-3.5" />} title="Fix — run these yourself">
        <ol className="space-y-3">
          {steps.map((s) => (
            <li key={s.order} className="flex gap-3">
              <span className="grid size-6 shrink-0 place-items-center rounded-full bg-surface-2 text-xs font-semibold text-muted">{s.order}</span>
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
      </Section>

      {d.verificationCommands.length > 0 && (
        <Section icon={<CheckCircle2 className="size-3.5" />} title="Verify">
          <div className="space-y-1.5">
            {d.verificationCommands.map((c, i) => (
              <Command key={i} cmd={c} />
            ))}
          </div>
        </Section>
      )}

      {d.rollbackGuidance && (
        <Section icon={<RotateCcw className="size-3.5" />} title="Rollback">
          {d.rollbackGuidance.trim().startsWith("kubectl ") ? <Command cmd={d.rollbackGuidance} write /> : <p className="text-sm">{d.rollbackGuidance}</p>}
        </Section>
      )}

      {d.documentationReferences.length > 0 && (
        <Section icon={<BookOpen className="size-3.5" />} title="Kubernetes docs">
          <ul className="grid gap-2 sm:grid-cols-2">
            {d.documentationReferences.map((r, i) => (
              <li key={i}>
                <a
                  href={r.url}
                  target="_blank"
                  rel="noreferrer"
                  className="group block h-full rounded-lg border border-line px-3 py-2 hover:border-accent/50 hover:bg-surface-2"
                >
                  <div className="flex items-center gap-1.5 text-sm font-medium group-hover:text-accent">
                    {r.title}
                    <ExternalLink className="size-3 opacity-60" />
                  </div>
                  {r.section && <div className="text-xs text-muted">{r.section}</div>}
                </a>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {d.uncertaintyNotes.length > 0 && (
        <Section icon={<AlertTriangle className="size-3.5" />} title="Caveats">
          <ul className="space-y-1 text-sm text-muted">
            {d.uncertaintyNotes.map((n, i) => (
              <li key={i}>• {n}</li>
            ))}
          </ul>
        </Section>
      )}
    </article>
  );
}
