import { useMutation } from "@tanstack/react-query";
import { CircleCheck, CircleX, Hourglass, RefreshCw, SearchX, Sparkles } from "lucide-react";
import { useEffect, useRef } from "react";
import type { ClusterTarget } from "../lib/api";
import { api } from "../lib/api";
import { failureLabel } from "../lib/format";
import type { Verdict, VerifyRequest, VerifyResult } from "../lib/types";
import FixSteps from "./FixSteps";
import { Badge, Button, ErrorBox } from "./ui";

const VERDICT: Record<Verdict, { label: string; tone: "ok" | "warn" | "bad" | "info" | "muted"; icon: typeof CircleCheck }> = {
  fixed: { label: "Fixed", tone: "ok", icon: CircleCheck },
  starting: { label: "Starting…", tone: "info", icon: Hourglass },
  still_failing: { label: "Still failing", tone: "bad", icon: CircleX },
  new_problem: { label: "New problem", tone: "warn", icon: Sparkles },
  not_found: { label: "Pod gone", tone: "muted", icon: SearchX },
};

/** While a replacement pod is starting, re-check on its own a few times. */
const AUTO_RECHECK_MS = 5_000;
const MAX_AUTO_RECHECKS = 12;

/** The verdict of one check, with the next fix when it isn't fixed. */
export function VerifyOutcome({ r }: { r: VerifyResult }) {
  const v = VERDICT[r.verdict];
  const Icon = v.icon;
  return (
    <div className="space-y-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={v.tone}>
          <Icon className="size-3.5" /> {v.label}
        </Badge>
        {r.pod && (
          <span className="font-mono text-xs text-muted">
            {r.pod.name} · {r.pod.status} · ready {r.pod.ready} · restarts {r.pod.restartCount}
          </span>
        )}
      </div>
      <p className="text-sm">{r.message}</p>
      {r.verdict !== "fixed" && r.evidence.length > 0 && (
        <ul className="space-y-1">
          {r.evidence.map((e, i) => (
            <li key={i} className="font-mono text-[12px] break-words text-muted">
              • {e}
            </li>
          ))}
        </ul>
      )}
      {r.fix?.steps?.length ? (
        <div className="space-y-2 border-t border-line pt-3">
          <div className="text-xs font-semibold uppercase tracking-wider text-muted">
            Next fix{r.failureType ? ` — ${failureLabel(r.failureType)}` : ""}
          </div>
          <FixSteps steps={r.fix.steps} />
        </div>
      ) : null}
    </div>
  );
}

/** "Did it work?" — after running the fix, re-check the pod (or the
 * new pod its workload rolled out) and compare with the diagnosis. */
export default function VerifyFix({ target, ns, name, req }: { target: ClusterTarget; ns: string; name: string; req: VerifyRequest }) {
  const autoChecks = useRef(0);
  const check = useMutation({ mutationFn: () => api.verify(target, ns, name, req) });
  const { mutate } = check;

  useEffect(() => {
    if (check.data?.verdict !== "starting" || autoChecks.current >= MAX_AUTO_RECHECKS) return;
    const t = setTimeout(() => {
      autoChecks.current += 1;
      mutate();
    }, AUTO_RECHECK_MS);
    return () => clearTimeout(t);
  }, [check.data, mutate]);

  return (
    <section className="rounded-xl border border-line bg-surface px-5 py-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold">Did the fix work?</h3>
          <p className="text-xs text-muted">After running the fix, check the pod again. If the fix rolled out a new pod, the check follows it.</p>
        </div>
        <Button
          variant={check.data ? "ghost" : "primary"}
          busy={check.isPending}
          onClick={() => {
            autoChecks.current = 0;
            check.mutate();
          }}
        >
          {!check.isPending && <RefreshCw className="size-4" />}
          {check.data ? "Check again" : "Check now"}
        </Button>
      </div>
      {check.isError && (
        <div className="mt-3">
          <ErrorBox error={check.error} />
        </div>
      )}
      {check.data && (
        <div className="mt-3">
          <VerifyOutcome r={check.data} />
        </div>
      )}
    </section>
  );
}
