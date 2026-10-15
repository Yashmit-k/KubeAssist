/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// In development the Vite server proxies /api to the Go backend, so the
// browser talks to a single origin (no CORS, SSE just works). Override
// the target with KUBEASSIST_API=http://host:port npm run dev.
const api = process.env.KUBEASSIST_API ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: api, changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
  },
});
