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

import { goDuration } from "./format";

describe("goDuration", () => {
  it.each([
    ["1m0s", "1 min"],
    ["30s", "30 s"],
    ["1h30m0s", "1 h 30 min"],
    ["2m30s", "2 min 30 s"],
    ["weird", "weird"],
  ])("%s -> %s", (d, want) => expect(goDuration(d)).toBe(want));
});
