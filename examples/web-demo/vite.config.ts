import { resolve } from "node:path";
import { defineConfig } from "vite";

// Two pages: the presenter/viewer demo (index.html) and the video call demo (call.html).
export default defineConfig({
  // Unknown paths 404 instead of falling back to index.html.
  appType: "mpa",
  build: {
    // hls.js (the recording player) is its own chunk, loaded on the first "Rewind".
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      input: { live: resolve(import.meta.dirname, "index.html"), call: resolve(import.meta.dirname, "call.html") },
    },
  },
});
