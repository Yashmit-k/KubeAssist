/**
 * A deliberately tiny markdown subset for chat answers: paragraphs,
 * "- " / "1. " lists, ```bash command blocks and ```log blocks, plus
 * **bold**, `code` and _italic_ inline. Parsed into data (never HTML),
 * so model or log text can't inject markup.
 */
export type Inline = { kind: "text" | "bold" | "code" | "italic"; text: string };
export type Block =
  | { kind: "para"; inline: Inline[] }
  | { kind: "list"; ordered: boolean; items: Inline[][] }
  | { kind: "command"; text: string }
  | { kind: "log"; text: string };

const INLINE = /(\*\*[^*]+\*\*|`[^`]+`|(?<![\w])_[^_]+_(?![\w]))/g;

export function parseInline(text: string): Inline[] {
  const out: Inline[] = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    const i = m.index ?? 0;
    if (i > last) out.push({ kind: "text", text: text.slice(last, i) });
    const t = m[0];
    if (t.startsWith("**")) out.push({ kind: "bold", text: t.slice(2, -2) });
    else if (t.startsWith("`")) out.push({ kind: "code", text: t.slice(1, -1) });
    else out.push({ kind: "italic", text: t.slice(1, -1) });
    last = i + t.length;
  }
  if (last < text.length) out.push({ kind: "text", text: text.slice(last) });
  return out;
}

const BULLET = /^\s*[-*]\s+/;
const NUMBERED = /^\s*\d+[.)]\s+/;

export function parseMarkdown(src: string): Block[] {
  const blocks: Block[] = [];
  const lines = src.replace(/\r\n/g, "\n").split("\n");
  let para: string[] = [];
  const flush = () => {
    if (para.length) blocks.push({ kind: "para", inline: parseInline(para.join(" ")) });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const fence = line.match(/^\s*```(\w*)\s*$/);
    if (fence) {
      flush();
      const body: string[] = [];
      for (i++; i < lines.length && !/^\s*```\s*$/.test(lines[i]); i++) body.push(lines[i]);
      const text = body.join("\n").trim();
      const lang = fence[1].toLowerCase();
      if (text) blocks.push({ kind: ["log", "text", "txt", ""].includes(lang) && !/^kubectl\b/.test(text) ? "log" : "command", text });
      continue;
    }
    if (BULLET.test(line) || NUMBERED.test(line)) {
      flush();
      const ordered = NUMBERED.test(line);
      const items: Inline[][] = [];
      for (; i < lines.length && (ordered ? NUMBERED : BULLET).test(lines[i]); i++) {
        items.push(parseInline(lines[i].replace(ordered ? NUMBERED : BULLET, "")));
      }
      i--;
      blocks.push({ kind: "list", ordered, items });
      continue;
    }
    if (!line.trim()) flush();
    else para.push(line.trim());
  }
  flush();
  return blocks;
}

const WRITE_VERBS = /^\s*kubectl\s+(?:-\S+\s+\S+\s+)*(apply|create|edit|patch|delete|replace|scale|set|label|annotate|rollout\s+(restart|undo)|exec|drain|cordon|taint)\b/;

/** Whether a suggested command changes the cluster (for the warning ring). */
export function isWriteCommand(cmd: string): boolean {
  return WRITE_VERBS.test(cmd);
}
