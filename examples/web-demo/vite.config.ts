import { resolve } from "node:path";
import { defineConfig } from "vite";

// Two pages: the video call demo (index.html) and the presenter/viewer demo (live.html).
export default defineConfig({
  build: {
    rollupOptions: {
      input: { call: resolve(import.meta.dirname, "index.html"), live: resolve(import.meta.dirname, "live.html") },
    },
  },
});
