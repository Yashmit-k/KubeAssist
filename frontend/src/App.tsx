import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import Layout from "./components/Layout";
import Connect from "./pages/Connect";
import Dashboard from "./pages/Dashboard";
import Diagnose from "./pages/Diagnose";
import { HistoryList, InvestigationDetail } from "./pages/History";
import Home from "./pages/Home";
import NotFound from "./pages/NotFound";
import PodDetail from "./pages/PodDetail";
import { ApiError } from "./lib/api";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      // Don't hammer the API with retries for client errors (401/404).
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
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
