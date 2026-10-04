import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The build output (web/dist) is committed and embedded in the Go binary:
// no network access is needed at run time (air-gapped production).
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    // Keep font files as separate hashed assets rather than inlined data URIs.
    assetsInlineLimit: 0,
    chunkSizeWarningLimit: 800,
  },
  server: {
    // `npm run dev` proxies the API to a local estopo.
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
});
