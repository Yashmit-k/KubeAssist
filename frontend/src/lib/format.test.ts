import { describe, expect, it } from "vitest";
import { failureTone, statusTone } from "./format";

describe("statusTone", () => {
  it.each([
    ["Running", "ok"],
    ["Completed", "ok"],
    ["Pending", "warn"],
    ["Init:0/1", "warn"],
    ["Init:CrashLoopBackOff", "bad"],
    ["CrashLoopBackOff", "bad"],
    ["ImagePullBackOff", "bad"],
    ["Evicted", "bad"],
  ])("%s -> %s", (status, tone) => expect(statusTone(status)).toBe(tone));
});

describe("failureTone", () => {
  it("treats Healthy as ok and Unknown as muted", () => {
    expect(failureTone("Healthy")).toBe("ok");
    expect(failureTone("Unknown")).toBe("muted");
    expect(failureTone("OOMKilled")).toBe("bad");
  });
});

import { podTone } from "./format";

describe("podTone", () => {
  it("flags Running pods that are not ready", () => {
    expect(podTone({ status: "Running", ready: "0/1" })).toBe("warn");
    expect(podTone({ status: "Running", ready: "2/2" })).toBe("ok");
  });
});
