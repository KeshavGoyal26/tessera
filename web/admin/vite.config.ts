import { defineConfig } from 'vite';

// The Go server embeds the build output (pkg/dashboard/dist) and serves it at /admin/.
export default defineConfig({
  base: './',
  build: { outDir: '../../pkg/dashboard/dist', emptyOutDir: true, chunkSizeWarningLimit: 700 },
  server: { proxy: { '/admin/api': 'http://localhost:7880' } },
});
