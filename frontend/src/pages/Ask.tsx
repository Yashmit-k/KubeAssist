import { useMutation } from "@tanstack/react-query";
import { Bot, CircleAlert, Eye, Send, User as UserIcon } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { useHealth } from "../components/Layout";
import AgentWaiting from "../components/AgentWaiting";
import RemoteGate from "../components/RemoteGate";
import { Badge, Button, Command, cx, ErrorBox, PageHeader } from "../components/ui";
import { api } from "../lib/api";
import { useActiveCluster, useAgentWait } from "../lib/clusters";
import { isWriteCommand, parseMarkdown, type Inline } from "../lib/markdown";
import type { ChatMessage, ChatToolUse } from "../lib/types";

const SUGGESTIONS = ["What's broken?", "What's wrong in kubeassist-lab?", "Why is oom-failure crashing?", "Show logs for crashloop-app"];

/** Turns of the conversation sent back to the API (text only). */
const MAX_HISTORY = 12;

type Turn = ChatMessage & { tools?: ChatToolUse[]; mode?: "llm" | "rules" };

function InlineText({ parts }: { parts: Inline[] }) {
  return (
    <>
      {parts.map((p, i) =>
        p.kind === "bold" ? (
          <strong key={i}>{p.text}</strong>
        ) : p.kind === "code" ? (
          <code key={i} className="rounded bg-surface-2 px-1 py-0.5 font-mono text-[12.5px]">
            {p.text}
          </code>
        ) : p.kind === "italic" ? (
          <em key={i} className="text-muted">
            {p.text}
          </em>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

/** Renders an answer from parsed blocks — never as HTML. */
function Answer({ text }: { text: string }) {
  return (
    <div className="space-y-2.5 text-sm leading-relaxed">
      {parseMarkdown(text).map((b, i) => {
        switch (b.kind) {
          case "command":
            return (
              <div key={i} className="space-y-1.5">
                {b.text.split("\n").filter(Boolean).map((cmd, j) => (
                  <Command key={j} cmd={cmd} write={isWriteCommand(cmd)} />
                ))}
              </div>
            );
          case "log":
            return (
              <pre key={i} className="max-h-72 overflow-auto rounded-lg bg-code-bg p-3 font-mono text-[11.5px] whitespace-pre-wrap text-code-fg">
                {b.text}
              </pre>
            );
          case "list": {
            const List = b.ordered ? "ol" : "ul";
            return (
              <List key={i} className={cx("space-y-1 pl-5", b.ordered ? "list-decimal" : "list-disc")}>
                {b.items.map((it, j) => (
                  <li key={j}>
                    <InlineText parts={it} />
                  </li>
                ))}
              </List>
            );
          }
          default:
            return (
              <p key={i}>
                <InlineText parts={b.inline} />
              </p>
            );
        }
      })}
    </div>
  );
}

function Bubble({ who, children }: { who: "user" | "assistant"; children: ReactNode }) {
  const mine = who === "user";
  return (
    <div className={cx("flex gap-3", mine && "flex-row-reverse")}>
      <span className={cx("grid size-7 shrink-0 place-items-center rounded-full", mine ? "bg-accent text-accent-fg" : "bg-surface-2 text-muted")}>
        {mine ? <UserIcon className="size-4" /> : <Bot className="size-4" />}
      </span>
      <div className={cx("min-w-0 max-w-[85%] rounded-xl border px-4 py-3", mine ? "border-accent/30 bg-accent-soft" : "border-line bg-surface")}>{children}</div>
    </div>
  );
}

/** Ask questions about the cluster. The AI picks read-only tools (scan,
 * diagnose, logs, lists), the backend runs them, and the answer cites
 * what it found with exact commands to run — nothing is executed. */
export default function Ask() {
  const cluster = useActiveCluster();
  const health = useHealth();
  const target = cluster.target;
  const [turns, setTurns] = useState<Turn[]>([]);
  const [input, setInput] = useState("");
  const end = useRef<HTMLDivElement>(null);

  const send = useMutation({
    mutationFn: (history: ChatMessage[]) => api.chat(target!, history),
    onSuccess: (r) => setTurns((t) => [...t, { role: "assistant", content: r.reply, tools: r.tools, mode: r.mode }]),
  });
  // A question that failed only because the agent was away is re-sent.
  const waiting = useAgentWait(target, [send.error], () => {
    if (send.isError && send.variables) send.mutate(send.variables);
  });

  // Block body on purpose: newer browsers return a Promise from
  // scrollIntoView, and React would call an effect's return value as its
  // cleanup.
  useEffect(() => {
    end.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [turns, send.isPending]);

  if (!target) return <RemoteGate clusterId={cluster.key} />;

  const ask = (q: string) => {
    const question = q.trim();
    if (!question || send.isPending) return;
    const next: Turn[] = [...turns, { role: "user", content: question }];
    setTurns(next);
    setInput("");
    send.mutate(next.slice(-MAX_HISTORY).map(({ role, content }) => ({ role, content })));
  };
  const aiOff = health.data?.ai === "disabled";

  return (
    <div className="flex min-h-[calc(100vh-6rem)] flex-col">
      <PageHeader title="Ask about this cluster" subtitle={`Read-only: I look at pods, events and logs in ${cluster.label} and tell you what to run.`} />

      {aiOff && (
        <div className="mb-3 flex items-center gap-2 rounded-lg border border-warn/40 bg-warn-soft px-3 py-2 text-sm">
          <CircleAlert className="size-4 text-warn" /> The AI service isn't configured on this backend, so chat is unavailable.
        </div>
      )}

      <div className="flex-1 space-y-4 pb-4">
        {turns.length === 0 && (
          <div className="rounded-xl border border-dashed border-line p-6 text-center">
            <Bot className="mx-auto mb-2 size-8 text-faint" />
            <p className="mb-3 text-sm text-muted">Ask in plain English. For example:</p>
            <div className="flex flex-wrap justify-center gap-2">
              {SUGGESTIONS.map((s) => (
                <button key={s} type="button" className="rounded-full border border-line px-3 py-1.5 text-sm hover:border-accent/50 hover:text-accent" onClick={() => ask(s)}>
                  {s}
                </button>
              ))}
            </div>
          </div>
        )}

        {turns.map((t, i) =>
          t.role === "user" ? (
            <Bubble key={i} who="user">
              <p className="text-sm whitespace-pre-wrap">{t.content}</p>
            </Bubble>
          ) : (
            <Bubble key={i} who="assistant">
              {t.tools && t.tools.length > 0 && (
                <div className="mb-2.5 flex flex-wrap gap-1.5">
                  {t.tools.map((tool, j) => (
                    <Badge key={j} tone={tool.ok ? "muted" : "warn"} title={JSON.stringify(tool.arguments)}>
                      <Eye className="size-3" /> {tool.summary}
                    </Badge>
                  ))}
                </div>
              )}
              <Answer text={t.content} />
              {t.mode === "rules" && (
                <p className="mt-2.5 border-t border-line pt-2 text-[11px] text-faint">
                  Rule-based answer (no language model configured on this backend) — built only from what the tools above found.
                </p>
              )}
            </Bubble>
          ),
        )}

        {send.isPending && (
          <Bubble who="assistant">
            <p className="animate-pulse text-sm text-muted">Looking at the cluster…</p>
          </Bubble>
        )}
        {waiting ? <AgentWaiting clusterId={cluster.key} /> : send.isError && <ErrorBox error={send.error} title="Couldn't answer" />}
        <div ref={end} />
      </div>

      <form
        className="sticky bottom-0 flex gap-2 border-t border-line bg-bg py-3"
        onSubmit={(e) => {
          e.preventDefault();
          ask(input);
        }}
      >
        <input
          className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-2 text-sm"
          placeholder="e.g. Why is checkout crashing?"
          maxLength={2000}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          disabled={aiOff}
        />
        <Button type="submit" variant="primary" busy={send.isPending} disabled={!input.trim() || aiOff}>
          {!send.isPending && <Send className="size-4" />} Ask
        </Button>
      </form>
    </div>
  );
}
