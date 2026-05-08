import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

// Build output is written to ../internal/ui/dist so that the Go service
// binary can embed it via go:embed all:dist.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: path.resolve(__dirname, "../internal/ui/dist"),
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
    },
  },
});
