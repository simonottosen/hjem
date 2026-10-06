import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import cssInjectedByJsPlugin from "vite-plugin-css-injected-by-js";
import path from "path";

export default defineConfig({
  plugins: [react(), tailwindcss(), cssInjectedByJsPlugin()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  base: "/dist/",
  build: {
    outDir: "dist",
    rollupOptions: {
      output: {
        entryFileNames: "app.bundle.js",
        // Hashless, because the server embeds whatever is in dist and the
        // entry point is served no-cache — a hashed chunk name would survive
        // a deploy in nobody's cache but still bloat the embedded binary with
        // every past build. Unhashed chunks need the entry's no-cache too,
        // which Routes() in api.go gives the whole /dist/ tree.
        chunkFileNames: "[name].bundle.js",
        assetFileNames: "assets/[name].[ext]",
      },
    },
  },
  server: {
    port: 3000,
    proxy: {
      "/api": "http://localhost:8080",
      "/download": "http://localhost:8080",
    },
  },
});
