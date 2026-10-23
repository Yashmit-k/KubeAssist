import { describe, expect, it } from "vitest";
import { isWriteCommand, parseInline, parseMarkdown } from "./markdown";

describe("parseMarkdown", () => {
  it("parses the chat subset", () => {
    const blocks = parseMarkdown(
      "I scanned **lab**: 2 pods.\n\n**1. ConfigMap `cfg` is missing key** (`a-1`)\n\nAdd it:\n\n```bash\nkubectl patch configmap cfg -n lab --type merge -p '{\"data\":{\"k\":\"<value>\"}}'\n```\n\n- one\n- two `x`\n\n```log\nfatal: db down\n```\n\n_note_",
    );
    expect(blocks.map((b) => b.kind)).toEqual(["para", "para", "para", "command", "list", "log", "para"]);
    expect(blocks[3]).toEqual({ kind: "command", text: "kubectl patch configmap cfg -n lab --type merge -p '{\"data\":{\"k\":\"<value>\"}}'" });
    expect(blocks[4]).toMatchObject({ kind: "list", ordered: false });
    expect(parseInline("a **b** `c` _d_")).toEqual([
      { kind: "text", text: "a " },
      { kind: "bold", text: "b" },
      { kind: "text", text: " " },
      { kind: "code", text: "c" },
      { kind: "text", text: " " },
      { kind: "italic", text: "d" },
    ]);
  });

  it("never treats snake_case or log text as markup it shouldn't", () => {
    expect(parseInline("my_pod_name")).toEqual([{ kind: "text", text: "my_pod_name" }]);
    expect(parseMarkdown("<script>alert(1)</script>")).toEqual([{ kind: "para", inline: [{ kind: "text", text: "<script>alert(1)</script>" }] }]);
  });

  it("flags cluster-changing commands", () => {
    expect(isWriteCommand("kubectl set resources deployment/x -n lab --limits=memory=64Mi")).toBe(true);
    expect(isWriteCommand("kubectl -n lab patch configmap x -p '{}'")).toBe(true);
    expect(isWriteCommand("kubectl describe pod x -n lab")).toBe(false);
    expect(isWriteCommand("kubectl logs x --previous")).toBe(false);
  });
});
