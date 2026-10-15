import { LogIn } from "lucide-react";
import { useMe } from "../lib/clusters";
import SignIn from "./SignIn";
import { Card, ErrorBox, Spinner } from "./ui";

/** Shown when the URL names a remote cluster but nobody is signed in:
 * remote clusters belong to accounts, so the user has to sign in. */
export default function RemoteGate({ clusterId }: { clusterId: string }) {
  const me = useMe();
  if (me.isLoading) return <Spinner />;
  if (me.isError) return <ErrorBox error={me.error} title="Could not check your session" />;
  return (
    <Card title={<span className="flex items-center gap-2"><LogIn className="size-4" /> Sign in to open “{clusterId}”</span>} className="mx-auto max-w-lg">
      <p className="mb-3 text-sm text-muted">Remote clusters belong to the account that connected them. Sign in to continue.</p>
      <SignIn />
    </Card>
  );
}
