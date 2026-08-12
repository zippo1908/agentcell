import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Built assets are embedded into celld (go:embed dist), so they are served
// from the same origin as the API — no CORS, no base path juggling.
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    // `pnpm dev` against a port-forwarded celld.
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/preview': 'http://127.0.0.1:8080',
      '/app': 'http://127.0.0.1:8080',
      '/login': 'http://127.0.0.1:8080',
    },
  },
})
