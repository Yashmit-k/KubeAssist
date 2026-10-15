import { KeyRound } from "lucide-react";
import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { savedClusters } from "../lib/clusters";
import { Button, Card, ErrorBox } from "./ui";

/** Shown when the URL names a remote cluster whose token isn't saved in
 * this browser: lets the user paste it (tokens are never re-issued). */
export default function RemoteGate({ clusterId }: { clusterId: string }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      await api.remoteStatus({ kind: "remote", clusterId, token: token.trim() });
      savedClusters.add(clusterId, token.trim());
    } catch (e) {
      setErr(e instanceof ApiError && e.status === 401 ? new Error("That token is not valid for this cluster.") : e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={<span className="flex items-center gap-2"><KeyRound className="size-4" /> Token required for “{clusterId}”</span>} className="mx-auto max-w-lg">
      <p className="mb-3 text-sm text-muted">
        This browser doesn't have the access token for this cluster. Paste the token you received when the cluster was registered.
      </p>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (token.trim()) void save();
        }}
      >
        <input
          type="password"
          autoComplete="off"
          className="w-full rounded-lg border border-line bg-surface px-3 py-2 font-mono text-sm"
          placeholder="cluster token"
          value={token}
          onChange={(e) => setToken(e.target.value)}
        />
        {err != null && <ErrorBox error={err} />}
        <Button variant="primary" type="submit" busy={busy} disabled={!token.trim()}>
          Save token
        </Button>
      </form>
    </Card>
  );
}
