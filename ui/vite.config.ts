import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build is embedded into the Go binary via internal/ui/embed.go, so Vite
// emits straight into internal/ui/dist. emptyOutDir is set explicitly because
// the outDir lives outside the Vite project root (the `make ui` target restores
// the committed dist/.gitkeep afterwards).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/ui/dist",
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` proxies API calls to a locally-running `qvr ui`.
    proxy: {
      "/api": "http://127.0.0.1:7878",
    },
  },
});
