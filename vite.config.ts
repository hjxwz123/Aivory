import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'node:path'

// §23 version probe: every production build gets a unique version id, baked
// into the bundle (__APP_VERSION__) AND emitted as dist/version.json. Open tabs
// compare the two and reload themselves at a safe moment after a deploy
// (src/lib/app-update.ts). CI can pin a stable id via GIT_SHA/GITHUB_SHA;
// otherwise the build timestamp is used (every build = a new version).
const appVersion = process.env.GIT_SHA || process.env.GITHUB_SHA || `build-${Date.now().toString(36)}`

// https://vitejs.dev/config/
export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  plugins: [
    react(),
    {
      name: 'aivory-emit-version-json',
      apply: 'build',
      generateBundle() {
        this.emitFile({
          type: 'asset',
          fileName: 'version.json',
          source: JSON.stringify({ version: appVersion }),
        })
      },
    },
  ],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    host: true,
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8787',
        changeOrigin: true,
        // SSE (fetch streaming) proxies fine over HTTP/1.1. ws:true forwards the
        // WebSocket upgrade for /api/audio/stream (live Volcano voice) to the Go
        // backend in dev; HMR runs on Vite's own socket, so this is scoped to
        // /api and doesn't touch it.
        ws: true,
      },
    },
  },
  build: {
    target: 'es2022',
    sourcemap: false,
    cssCodeSplit: true,
  },
  worker: {
    format: 'es',
  },
})
