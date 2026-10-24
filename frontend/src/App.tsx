import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import Layout from "./components/Layout";
import Alerts from "./pages/Alerts";
import Ask from "./pages/Ask";
import Connect from "./pages/Connect";
import Dashboard from "./pages/Dashboard";
import Diagnose from "./pages/Diagnose";
import { HistoryList, InvestigationDetail } from "./pages/History";
import HealthScan from "./pages/HealthScan";
import Home from "./pages/Home";
import NotFound from "./pages/NotFound";
import PodDetail from "./pages/PodDetail";
import { shouldRetry } from "./lib/api";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      retry: shouldRetry,
    },
  },
});

// /c/:cluster/... — ":cluster" is "local" or a remote cluster ID, so the
// same pages serve both kinds of cluster.
const router = createBrowserRouter([
  {
    element: <Layout />,
    children: [
      { index: true, element: <Home /> },
      { path: "connect", element: <Connect /> },
      {
        path: "c/:cluster",
        children: [
          { index: true, element: <Dashboard /> },
          { path: "pods", element: <Dashboard /> },
          { path: "scan", element: <HealthScan /> },
          { path: "ask", element: <Ask /> },
          { path: "alerts", element: <Alerts /> },
          { path: "pods/:ns/:name", element: <PodDetail /> },
          { path: "pods/:ns/:name/diagnose", element: <Diagnose /> },
          { path: "history", element: <HistoryList /> },
          { path: "history/:id", element: <InvestigationDetail /> },
        ],
      },
      { path: "*", element: <NotFound /> },
    ],
  },
]);

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}
