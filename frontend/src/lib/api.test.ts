import { afterEach, describe, expect, it, vi } from "vitest";
import { api, troubleshootStream, type StreamHandlers } from "./api";
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
    const res = new Response(JSON.stringify({ code: "unauthenticated", message: "sign in" }), { status: 401 });
    const r = await run(res);
    expect(r.error?.message).toBe("sign in");
  });

  it("uses the remote path and the session cookie (never a token header) for remote clusters", async () => {
    const r = await run(['event: done\ndata: {"failureType":"Healthy"}\n\n'], { kind: "remote", clusterId: "prod-eu" });
    expect(r.url).toBe("/api/remote/prod-eu/pods/shop/api-1/troubleshoot/stream");
    expect(r.init?.credentials).toBe("include");
    expect((r.init?.headers as Record<string, string>).Authorization).toBeUndefined();
  });
});

describe("api.me", () => {
  it("returns null when signed out and the user when signed in", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ code: "unauthenticated", message: "no" }), { status: 401 })));
    expect(await api.me()).toBeNull();
    const calls: RequestInit[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_u: string, i: RequestInit) => {
      calls.push(i);
      return new Response(JSON.stringify({ id: "u1", provider: "github", login: "octocat" }), { status: 200 });
    }));
    expect((await api.me())?.login).toBe("octocat");
    expect(calls[0].credentials).toBe("include");
  });

  it("propagates non-auth failures", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 503 })));
    await expect(api.me()).rejects.toMatchObject({ status: 503 });
  });

  it("builds a safe sign-in URL", () => {
    expect(api.loginUrl("github", "/c/prod eu/pods")).toBe("/api/auth/github/login?next=%2Fc%2Fprod%20eu%2Fpods");
  });
});
