import { QueryClient, useMutation, useQueryClient } from "@tanstack/react-query";
import { LogOut, Terminal } from "lucide-react";
import { useState } from "react";
import { useLocation } from "react-router";
import { api } from "../lib/api";
import type { User } from "../lib/types";
import { useHealth } from "./Layout";
import { Button, buttonClass, ErrorBox } from "./ui";

function GitHubMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" className={className}>
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  );
}

/** Records the new session user and drops everything cached for the
 * previous one (reset, not clear, so mounted queries refetch). */
function switchUser(qc: QueryClient, user: User | null) {
  qc.setQueryData(["me"], user);
  void qc.resetQueries({ predicate: (q) => q.queryKey[0] !== "me" && q.queryKey[0] !== "health" });
}

const AUTH_ERRORS: Record<string, string> = {
  access_denied: "Sign-in was cancelled.",
  login_expired: "The sign-in took too long. Please try again.",
  invalid_state: "The sign-in could not be verified. Please try again.",
};

/** Explains an `authError` the OAuth callback put on the URL. */
export function authErrorMessage(code: string | null): string | null {
  if (!code) return null;
  return AUTH_ERRORS[code] ?? `Sign-in failed (${code}). Please try again.`;
}

/** Sign-in buttons for every provider the backend enabled. */
export default function SignIn({ next }: { next?: string }) {
  const health = useHealth();
  const location = useLocation();
  const qc = useQueryClient();
  const [login, setLogin] = useState("");
  const dev = useMutation({
    mutationFn: () => api.devLogin(login.trim().toLowerCase()),
    onSuccess: (user) => switchUser(qc, user),
  });
  const providers = health.data?.authProviders ?? [];
  const target = next ?? location.pathname + location.search;

  if (!providers.length) {
    return <p className="text-sm text-muted">No sign-in method is configured on this backend (set GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET).</p>;
  }
  return (
    <div className="space-y-3">
      {providers.includes("github") && (
        <a href={api.loginUrl("github", target)} className={buttonClass("primary")}>
          <GitHubMark className="size-4" /> Sign in with GitHub
        </a>
      )}
      {providers.includes("dev") && (
        <form
          className="space-y-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (login.trim()) dev.mutate();
          }}
        >
          <div className="flex items-center gap-1.5 text-xs text-warn">
            <Terminal className="size-3.5" /> Development sign-in (no password) is enabled on this backend.
          </div>
          <div className="flex gap-2">
            <input
              className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-sm"
              placeholder="username"
              autoComplete="username"
              value={login}
              onChange={(e) => setLogin(e.target.value)}
            />
            <Button type="submit" busy={dev.isPending} disabled={!login.trim()}>
              Sign in
            </Button>
          </div>
          {dev.isError && <ErrorBox error={dev.error} />}
        </form>
      )}
    </div>
  );
}

/** The signed-in user with a sign-out button (sidebar footer). */
export function UserBadge({ user }: { user: User }) {
  const qc = useQueryClient();
  const logout = useMutation({
    mutationFn: api.logout,
    onSettled: () => switchUser(qc, null),
  });
  return (
    <div className="flex items-center gap-2 text-xs">
      {user.avatarUrl ? (
        <img src={user.avatarUrl} alt="" className="size-6 rounded-full" />
      ) : (
        <span className="grid size-6 place-items-center rounded-full bg-surface-2 font-medium uppercase">{user.login.slice(0, 1)}</span>
      )}
      <span className="min-w-0 flex-1 truncate" title={user.name || user.login}>
        {user.login}
      </span>
      <Button variant="ghost" className="px-1.5 py-1 text-xs" busy={logout.isPending} onClick={() => logout.mutate()} title="Sign out">
        <LogOut className="size-3.5" />
      </Button>
    </div>
  );
}
