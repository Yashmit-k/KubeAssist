import { describe, expect, it } from "vitest";
import { commandParts, hasPlaceholder, installCommand } from "./commands";

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
