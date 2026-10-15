import { Navigate } from "react-router";
import { useHealth } from "../components/Layout";
import { ErrorBox, Spinner } from "../components/ui";
import { LOCAL, useMe, useMyClusters } from "../lib/clusters";

/** Sends the user to the most sensible starting point. */
export default function Home() {
  const health = useHealth();
  const me = useMe();
  const mine = useMyClusters();
  if (health.isLoading) return <Spinner label="Connecting to the backend…" />;
  if (health.isError) return <ErrorBox error={health.error} title="The KubeAssist API is not reachable" />;
  if (health.data?.localMode) return <Navigate to={`/c/${LOCAL}/pods`} replace />;
  if (me.isLoading || (me.data && mine.isLoading)) return <Spinner />;
  const first = mine.data?.[0];
  if (first) return <Navigate to={`/c/${encodeURIComponent(first.clusterId)}/pods`} replace />;
  return <Navigate to="/connect" replace />;
}
