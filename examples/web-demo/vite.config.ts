import { resolve } from "node:path";
import { defineConfig } from "vite";

// Two pages: the presenter/viewer demo (index.html) and the video call demo (call.html).
export default defineConfig({
  build: {
    rollupOptions: {
      input: { live: resolve(import.meta.dirname, "index.html"), call: resolve(import.meta.dirname, "call.html") },
    },
  },
});
