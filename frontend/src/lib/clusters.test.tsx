import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, isAgentNotConnected, shouldRetry, type ClusterTarget } from "./api";
import { AGENT_POLL_MS, targetKey, useAgentWait } from "./clusters";

const notConnected = () => new ApiError(503, "agent_not_connected", "the agent for this cluster is not connected");

describe("isAgentNotConnected / shouldRetry", () => {
  it("recognizes only the 503 agent_not_connected error", () => {
    expect(isAgentNotConnected(notConnected())).toBe(true);
    expect(isAgentNotConnected(new ApiError(503, "unavailable", "x"))).toBe(false);
    expect(isAgentNotConnected(new Error("agent_not_connected"))).toBe(false);
  });

  it("doesn't retry client errors or a missing agent", () => {
    expect(shouldRetry(0, new ApiError(401, "unauthorized", "x"))).toBe(false);
    expect(shouldRetry(0, new ApiError(404, "not_found", "x"))).toBe(false);
    expect(shouldRetry(0, notConnected())).toBe(false);
  });

  it("retries other errors twice", () => {
    const err = new ApiError(502, "cluster_error", "x");
    expect(shouldRetry(0, err)).toBe(true);
    expect(shouldRetry(1, err)).toBe(true);
    expect(shouldRetry(2, err)).toBe(false);
  });
});

describe("useAgentWait", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  const target: ClusterTarget = { kind: "remote", clusterId: "test" };

  function setup(podsFn: () => Promise<string[]>, onConnected?: () => void) {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: shouldRetry, retryDelay: 0 } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    return renderHook(
      () => {
        const pods = useQuery({ queryKey: ["pods", targetKey(target)], queryFn: podsFn });
        const waiting = useAgentWait(target, [pods.error], onConnected);
        return { pods, waiting };
      },
      { wrapper },
    );
  }

  it("waits while the agent is away and refetches once it connects", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let agentUp = false;
    const podsFn = vi.fn(async () => {
      if (!agentUp) throw notConnected();
      return ["web-1"];
    });
    const status = vi.spyOn(api, "remoteStatus").mockImplementation(async (id) => ({ clusterId: id, connected: agentUp }));
    const onConnected = vi.fn();

    const { result } = setup(podsFn, onConnected);
    await waitFor(() => expect(result.current.waiting).toBe(true));
    expect(podsFn).toHaveBeenCalledTimes(1); // no retries on agent_not_connected
    await waitFor(() => expect(status).toHaveBeenCalledWith("test"));
    expect(onConnected).not.toHaveBeenCalled();

    agentUp = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(AGENT_POLL_MS);
    });
    await waitFor(() => expect(result.current.pods.data).toEqual(["web-1"]));
    expect(result.current.waiting).toBe(false);
    expect(onConnected).toHaveBeenCalled();
  });

  it("doesn't wait (or poll) on other errors", async () => {
    const status = vi.spyOn(api, "remoteStatus");
    const { result } = setup(async () => {
      throw new ApiError(404, "not_found", "no such cluster");
    });
    await waitFor(() => expect(result.current.pods.isError).toBe(true));
    expect(result.current.waiting).toBe(false);
    expect(status).not.toHaveBeenCalled();
  });

  it("never waits for the local cluster", async () => {
    const qc = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
    const { result } = renderHook(() => useAgentWait({ kind: "local" }, [notConnected()]), { wrapper });
    expect(result.current).toBe(false);
  });
});
