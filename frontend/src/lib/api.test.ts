import { afterEach, describe, expect, it, vi } from "vitest";
import { troubleshootStream, type StreamHandlers } from "./api";
import type { StageEvent, TroubleshootResult } from "./types";

function sseResponse(chunks: string[], status = 200): Response {
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      chunks.forEach((ch) => c.enqueue(enc.encode(ch)));
      c.close();
    },
  });
  return new Response(body, { status, headers: { "Content-Type": "text/event-stream" } });
}

function run(chunks: string[] | Response, target: Parameters<typeof troubleshootStream>[0] = { kind: "local" }) {
  const stages: StageEvent[] = [];
  return new Promise<{ stages: StageEvent[]; done?: TroubleshootResult; error?: Error; init?: RequestInit; url?: string }>((resolve) => {
    let init: RequestInit | undefined;
    let url: string | undefined;
    vi.stubGlobal("fetch", vi.fn(async (u: string, i: RequestInit) => {
      url = u;
      init = i;
      return Array.isArray(chunks) ? sseResponse(chunks) : chunks;
    }));
    const h: StreamHandlers = {
      onStage: (e) => stages.push(e),
      onDone: (done) => resolve({ stages, done, init, url }),
      onError: (error) => resolve({ stages, error, init, url }),
    };
    troubleshootStream(target, "shop", "api-1", { question: "why?" }, h);
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("troubleshootStream", () => {
  it("parses stages, ignores keepalives, handles events split across chunks", async () => {
    const done = JSON.stringify({ failureType: "CrashLoopBackOff", issueSummary: "x" });
    const r = await run([
      'data: {"stage":"collecting","message":"Collecting"}\n\n',
      ": keepalive\n\n",
      'data: {"stage":"detec',
      'ting"}\n\nevent: done\n',
      `data: ${done}\n\n`,
    ]);
    expect(r.stages.map((s) => s.stage)).toEqual(["collecting", "detecting"]);
    expect(r.done?.failureType).toBe("CrashLoopBackOff");
    expect(r.init?.method).toBe("POST");
    expect(JSON.parse(String(r.init?.body))).toEqual({ question: "why?" });
  });

  it("reports server-sent error events", async () => {
    const r = await run(['event: error\ndata: {"code":"pod_not_found","message":"nope"}\n\n']);
    expect(r.error?.message).toBe("nope");
  });

  it("reports a stream that ends without a result", async () => {
    const r = await run(['data: {"stage":"collecting"}\n\n']);
    expect(r.error?.message).toMatch(/closed before/);
  });

  it("surfaces JSON error bodies from non-200 responses", async () => {
    const res = new Response(JSON.stringify({ code: "invalid_cluster_token", message: "bad token" }), { status: 401 });
    const r = await run(res);
    expect(r.error?.message).toBe("bad token");
  });

  it("sends the cluster token and remote path for remote clusters", async () => {
    const r = await run(['event: done\ndata: {"failureType":"Healthy"}\n\n'], { kind: "remote", clusterId: "prod-eu", token: "t0k" });
    expect(r.url).toBe("/api/remote/prod-eu/pods/shop/api-1/troubleshoot/stream");
    expect((r.init?.headers as Record<string, string>).Authorization).toBe("Bearer t0k");
  });
});
