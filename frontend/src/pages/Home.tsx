import { Navigate } from "react-router";
import { useHealth } from "../components/Layout";
import { ErrorBox, Spinner } from "../components/ui";
import { LOCAL, useSavedClusters } from "../lib/clusters";

/** Sends the user to the most sensible starting point. */
export default function Home() {
  const health = useHealth();
  const saved = useSavedClusters();
  if (health.isLoading) return <Spinner label="Connecting to the backend…" />;
  if (health.isError) return <ErrorBox error={health.error} title="The KubeAssist API is not reachable" />;
  if (health.data?.localMode) return <Navigate to={`/c/${LOCAL}/pods`} replace />;
  if (saved.length) return <Navigate to={`/c/${encodeURIComponent(saved[0].clusterId)}/pods`} replace />;
  return <Navigate to="/connect" replace />;
}
