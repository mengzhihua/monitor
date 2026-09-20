import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

// Build output is embedded into the Go binary (core/internal/api/ui/dist).
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../core/internal/api/ui/dist',
    emptyOutDir: false,
  },
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:19999', ws: true },
      '/metrics': 'http://127.0.0.1:19999',
    },
  },
})
