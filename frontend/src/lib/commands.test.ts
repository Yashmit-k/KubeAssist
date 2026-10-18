import { describe, expect, it } from "vitest";
import { applyFromDownloads, commandParts, detectShell, hasPlaceholder, installCommand } from "./commands";

describe("commandParts", () => {
  it("marks placeholders and keeps the rest verbatim", () => {
    const cmd = "kubectl patch configmap app -n shop --type merge -p '{\"data\":{\"db-url\":\"<value>\"}}'";
    const parts = commandParts(cmd);
    expect(parts.map((p) => p.text).join("")).toBe(cmd);
    expect(parts.filter((p) => p.placeholder).map((p) => p.text)).toEqual(["<value>"]);
  });

  it("finds several and ignores shell redirection", () => {
    expect(commandParts("set image deploy/a app=<registry>/x:<tag>").filter((p) => p.placeholder).map((p) => p.text)).toEqual([
      "<registry>",
      "<tag>",
    ]);
    expect(hasPlaceholder("kubectl get pod x -o yaml > x.yaml")).toBe(false);
    expect(hasPlaceholder("kubectl apply -f - <<'EOF'")).toBe(false);
  });
});

describe("installCommand", () => {
  it("wraps the manifest in a quoted heredoc", () => {
    const cmd = installCommand("kind: Namespace\nmetadata:\n  name: kubeassist\n");
    expect(cmd).toBe("kubectl apply -f - <<'KUBEASSIST_EOF'\nkind: Namespace\nmetadata:\n  name: kubeassist\nKUBEASSIST_EOF\n");
  });

  it("never lets the manifest end the heredoc early", () => {
    const cmd = installCommand("a: 1\nKUBEASSIST_EOF\nb: 2");
    expect(cmd.startsWith("kubectl apply -f - <<'KUBEASSIST_EOF_'\n")).toBe(true);
    expect(cmd.endsWith("b: 2\nKUBEASSIST_EOF_\n")).toBe(true);
  });
});

describe("detectShell", () => {
  it.each([
    ["Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/140", undefined, "powershell"],
    ["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", undefined, "posix"],
    ["Mozilla/5.0 (X11; Linux x86_64)", undefined, "posix"],
    ["anything", "Windows", "powershell"],
    ["anything", "macOS", "posix"],
  ])("%s / %s -> %s", (userAgent, platform, want) => {
    expect(detectShell({ userAgent, userAgentData: platform ? { platform } : undefined })).toBe(want);
  });
});

describe("applyFromDownloads", () => {
  it("points at the Downloads folder in each shell's syntax", () => {
    expect(applyFromDownloads("kubeassist-agent-prod.yaml", "powershell")).toBe('kubectl apply -f "$HOME\\Downloads\\kubeassist-agent-prod.yaml"');
    expect(applyFromDownloads("kubeassist-agent-prod.yaml", "posix")).toBe("kubectl apply -f ~/Downloads/kubeassist-agent-prod.yaml");
  });
});
