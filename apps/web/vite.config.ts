import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { appVersion } from './build-version'

// The build output lands directly in the Go server's embed directory, so
// `make build` produces one binary with the PWA inside it.
export default defineConfig({
  // Stamp the version into the bundle — the browser has no git to ask.
  define: { __APP_VERSION__: JSON.stringify(appVersion()) },
  plugins: [react()],
  build: {
    outDir: '../../server/internal/web/dist',
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` talks to a server started with `make run`.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8877',
        changeOrigin: true,
      },
    },
  },
})
