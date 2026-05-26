import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// In dev (`npm run dev`), Vite serves on :5173 and proxies the WebSocket to
// the Go daemon so the browser talks to one origin. In production the Go
// daemon serves the built assets from client/dist and the socket is
// same-origin, so no proxy is involved.
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      "/ws": {
        target: "http://127.0.0.1:8080",
        ws: true,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
  },
});
