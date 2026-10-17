import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { scaled } from "./test-scale";

// In development the Go server runs on 127.0.0.1:8098 and Vite proxies API and
// auth routes to it, so the browser sees a single origin.
const backend = "http://127.0.0.1:8098";

export default defineConfig({
  plugins: [react()],
  // "@/…" is src/ (bitop-ui components are installed under src/components/ui by the bitop CLI).
  resolve: { alias: [{ find: /^@\//, replacement: "/src/" }] },
  server: {
    port: 5176,
    proxy: {
      "/v1": { target: backend, changeOrigin: true },
      "/auth": { target: backend, changeOrigin: true },
      "/healthz": { target: backend, changeOrigin: true },
      "/readyz": { target: backend, changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    // dist/.gitkeep (embed.go needs the directory) comes back from public/.gitkeep on every build.
    emptyOutDir: true,
    sourcemap: false,
    rolldownOptions: {
      output: {
        // Pages are split by route (router.tsx). Libraries the shell needs go
        // into their own chunks, which change rarely and cache well.
        codeSplitting: {
          groups: [
            { name: "react", test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/, priority: 30 },
            { name: "base-ui", test: /node_modules[\\/](@base-ui|@floating-ui|tabbable)[\\/]/, priority: 20 },
            { name: "tanstack", test: /node_modules[\\/]@tanstack[\\/]/, priority: 20 },
          ],
        },
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    // Headroom for loaded CI runners (jsdom). A limit, not a delay.
    // times TEST_TIME_SCALE (1 unless set, test-scale.ts) for slower or shared machines
    testTimeout: scaled(20_000),
  },
});
