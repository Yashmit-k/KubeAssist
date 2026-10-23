import { useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, History, Loader2, Stethoscope, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import ReportButtons from "../components/ReportButtons";
import VerifyFix from "../components/VerifyFix";
import DiagnosisView from "../components/DiagnosisView";
import RemoteGate from "../components/RemoteGate";
import { Button, buttonClass, Card, cx, ErrorBox, PageHeader } from "../components/ui";
import { troubleshootStream, type ApiError } from "../lib/api";
import { useActiveCluster } from "../lib/clusters";
import { diagnosisReport, reportFileName } from "../lib/report";
import type { StageEvent, TroubleshootResult, VerifyResult } from "../lib/types";

// The Go pipeline's stages, in order. "related" (pass-2) and "saving"
// only happen sometimes; they're shown when the server reports them.
const STAGES: { id: string; label: string }[] = [
  { id: "collecting", label: "Collect evidence" },
  { id: "detecting", label: "Classify failure" },
  { id: "related", label: "Check related resources" },
  { id: "diagnosing", label: "Retrieve docs & diagnose" },
  { id: "saving", label: "Save investigation" },
];

type Run = { status: "idle" | "running" | "done" | "error"; events: StageEvent[]; result?: TroubleshootResult; error?: ApiError };

function Progress({ run }: { run: Run }) {
  const seen = new Map(run.events.map((e) => [e.stage, e]));
  const current = run.events.at(-1)?.stage;
  const visible = STAGES.filter((s) => seen.has(s.id) || (s.id !== "related" && s.id !== "saving"));
  return (
    <ol className="space-y-2.5">
      {visible.map((s) => {
        const ev = seen.get(s.id);
        const active = run.status === "running" && current === s.id;
        const done = !!ev && !active;
        const failed = run.status === "error" && current === s.id;
        return (
          <li key={s.id} className="flex items-start gap-3">
            <span
              className={cx(
                "mt-0.5 grid size-5 shrink-0 place-items-center rounded-full border",
                failed ? "border-bad bg-bad-soft text-bad" : done ? "border-ok bg-ok-soft text-ok" : active ? "border-accent text-accent" : "border-line text-faint",
              )}
            >
              {failed ? <X className="size-3" /> : done ? <Check className="size-3" /> : active ? <Loader2 className="size-3 animate-spin" /> : null}
            </span>
            <div className="min-w-0">
              <div className={cx("text-sm", !ev && "text-faint")}>{s.label}</div>
              {ev?.message && <div className="text-xs text-muted">{ev.message}</div>}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

export default function Diagnose() {
  const { ns = "", name = "" } = useParams();
  const [params] = useSearchParams();
  const cluster = useActiveCluster();
  const target = cluster.target;
  const qc = useQueryClient();

  const [question, setQuestion] = useState("");
  const [serviceName, setServiceName] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [run, setRun] = useState<Run>({ status: "idle", events: [] });
  const [verified, setVerified] = useState<VerifyResult | null>(null);
  const cancel = useRef<(() => void) | null>(null);

  const start = useCallback(() => {
    if (!target) return;
    cancel.current?.();
    setRun({ status: "running", events: [] });
    setVerified(null);
    cancel.current = troubleshootStream(
      target,
      ns,
      name,
      { question: question.trim() || undefined, serviceName: serviceName.trim() || undefined },
      {
        onStage: (e) => setRun((r) => ({ ...r, events: [...r.events.filter((x) => x.stage !== e.stage), e] })),
        onDone: (result) => {
          setRun((r) => ({ ...r, status: "done", result }));
          void qc.invalidateQueries({ queryKey: ["investigations"] });
        },
        onError: (error) => setRun((r) => ({ ...r, status: "error", error })),
      },
    );
  }, [target, ns, name, question, serviceName, qc]);

  // Quick "Diagnose" links from the pod table start immediately.
  const autoStarted = useRef(false);
  useEffect(() => {
    if (params.get("run") === "1" && target && !autoStarted.current) {
      autoStarted.current = true;
      start();
    }
  }, [params, target, start]);
  useEffect(() => () => cancel.current?.(), []);

  if (!target) return <RemoteGate clusterId={cluster.key} />;
  const base = `/c/${encodeURIComponent(cluster.key)}`;
  const podHref = `${base}/pods/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;

  return (
    <>
      <PageHeader
        title={`Diagnose ${name}`}
        crumbs={
          <>
            <Link to={`${base}/pods`} className="hover:text-fg">
              Pods
            </Link>{" "}
            / {ns} /{" "}
            <Link to={podHref} className="hover:text-fg">
              {name}
            </Link>
          </>
        }
        subtitle="Evidence is collected and classified deterministically; the AI only explains it. Nothing is ever run against your cluster."
      />

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="space-y-4 lg:col-span-1">
          <Card>
            <form
              className="space-y-3"
              onSubmit={(e) => {
                e.preventDefault();
                start();
              }}
            >
              <label className="block">
                <span className="mb-1 block text-sm font-medium">Question (optional)</span>
                <textarea
                  rows={3}
                  maxLength={1000}
                  className="w-full resize-y rounded-lg border border-line bg-surface px-3 py-2 text-sm"
                  placeholder="e.g. Why does this keep restarting after the last deploy?"
                  value={question}
                  onChange={(e) => setQuestion(e.target.value)}
                />
              </label>
              <button type="button" className="flex items-center gap-1 text-xs text-muted hover:text-fg" onClick={() => setAdvanced((a) => !a)}>
                <ChevronDown className={cx("size-3.5 transition", advanced && "rotate-180")} /> Advanced
              </button>
              {advanced && (
                <label className="block">
                  <span className="mb-1 block text-sm font-medium">Service to check</span>
                  <input
                    className="w-full rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm"
                    placeholder="service name"
                    value={serviceName}
                    onChange={(e) => setServiceName(e.target.value)}
                  />
                  <span className="mt-1 block text-xs text-muted">Checks this Service's selector and endpoints against the pod (otherwise every Service in the namespace is checked when the pod looks healthy).</span>
                </label>
              )}
              <Button type="submit" variant="primary" busy={run.status === "running"} className="w-full">
                {run.status !== "running" && <Stethoscope className="size-4" />}
                {run.status === "running" ? "Diagnosing…" : run.status === "idle" ? "Run diagnosis" : "Run again"}
              </Button>
            </form>
          </Card>

          {run.status !== "idle" && (
            <Card title="Progress">
              <Progress run={run} />
            </Card>
          )}
        </div>

        <div className="lg:col-span-2">
          {run.status === "error" && run.error && <ErrorBox error={run.error} title="Diagnosis failed" />}
          {run.result ? (
            <div className="space-y-3">
              <DiagnosisView d={run.result} aiError={run.result.aiError} />
              {run.result.failureType !== "Healthy" && (
                <VerifyFix
                  target={target}
                  ns={ns}
                  name={name}
                  req={{ failureType: run.result.failureType, owner: run.result.detector.fix?.facts?.owner, serviceName: serviceName.trim() || undefined }}
                  onResult={setVerified}
                />
              )}
              <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-line bg-surface px-4 py-3">
                <span className="text-sm text-muted">Incident report{verified ? " (includes the latest check)" : ""}</span>
                <ReportButtons
                  filename={reportFileName(ns, name)}
                  markdown={diagnosisReport({ cluster: cluster.label, namespace: ns, pod: name, diagnosis: run.result, question: question.trim() || undefined, verify: verified })}
                />
              </div>
              {run.result.investigationId && (
                <Link to={`${base}/history/${run.result.investigationId}`} className={buttonClass("ghost", "text-xs")}>
                  <History className="size-3.5" /> Saved to history
                </Link>
              )}
            </div>
          ) : (
            run.status !== "error" && (
              <div className="grid h-full min-h-60 place-items-center rounded-xl border border-dashed border-line p-8 text-center text-sm text-muted">
                {run.status === "running" ? "Working on it — this usually takes a few seconds." : "Run a diagnosis to see what failed, why, and how to fix it."}
              </div>
            )
          )}
        </div>
      </div>
    </>
  );
}
