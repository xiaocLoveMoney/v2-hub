import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const DEV_PORT = 5174
const HUB_SERVER_ORIGIN = 'http://127.0.0.1:8080'

// 开发时 /api、/sub 与 /healthz 代理到本机 hub-server；生产由 Caddy 同域反代，前端不感知后端地址。
export default defineConfig({
  plugins: [react()],
  server: {
    port: DEV_PORT,
    strictPort: true,
    proxy: {
      '/api': HUB_SERVER_ORIGIN,
      '/sub': HUB_SERVER_ORIGIN,
      '/healthz': HUB_SERVER_ORIGIN,
    },
  },
})
