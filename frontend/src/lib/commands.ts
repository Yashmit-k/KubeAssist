/**
 * Shell-command helpers for the UI. Nothing here runs anything: these
 * only shape text the user copies into their own terminal.
 */

/** A value the evidence couldn't know, written as <placeholder>. */
const PLACEHOLDER = /<[a-z][a-z0-9 _-]*>/gi;

export type CommandPart = { text: string; placeholder: boolean };

/** Splits a command so placeholders can be highlighted. */
export function commandParts(cmd: string): CommandPart[] {
  const parts: CommandPart[] = [];
  let last = 0;
  for (const m of cmd.matchAll(PLACEHOLDER)) {
    const i = m.index ?? 0;
    if (i > last) parts.push({ text: cmd.slice(last, i), placeholder: false });
    parts.push({ text: m[0], placeholder: true });
    last = i + m[0].length;
  }
  if (last < cmd.length) parts.push({ text: cmd.slice(last), placeholder: false });
  return parts;
}

export function hasPlaceholder(cmd: string): boolean {
  return commandParts(cmd).some((p) => p.placeholder);
}

/**
 * One paste-able command that applies a manifest: `kubectl apply -f -`
 * fed by a quoted heredoc, so nothing is expanded by the shell and no
 * file has to be saved first. Works in bash and zsh (macOS, Linux, WSL).
 */
export function installCommand(manifest: string): string {
  let delim = "KUBEASSIST_EOF";
  while (manifest.split("\n").some((l) => l.trim() === delim)) delim += "_";
  const body = manifest.endsWith("\n") ? manifest : manifest + "\n";
  return `kubectl apply -f - <<'${delim}'\n${body}${delim}\n`;
}

export type Shell = "powershell" | "posix";

/** Best guess at the visitor's shell: PowerShell on Windows, otherwise
 * bash/zsh. Only picks which instructions are shown first. */
export function detectShell(nav: Pick<Navigator, "userAgent"> & { userAgentData?: { platform?: string } } = navigator): Shell {
  const platform = nav.userAgentData?.platform ?? nav.userAgent;
  return /win/i.test(platform) && !/darwin|mac/i.test(platform) ? "powershell" : "posix";
}

/** `kubectl apply` for a manifest saved to the browser's default
 * Downloads folder, written for the given shell. */
export function applyFromDownloads(file: string, shell: Shell): string {
  return shell === "powershell" ? `kubectl apply -f "$HOME\\Downloads\\${file}"` : `kubectl apply -f ~/Downloads/${file}`;
}
