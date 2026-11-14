import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const DEV_PORT = 5173
const HUB_SERVER_ORIGIN = 'http://127.0.0.1:8080'
/** 生产单域名时管理端挂在 /admin/ 下（Ruling-61）；开发服务器仍在根路径。路由 basename 跟随 import.meta.env.BASE_URL。 */
const PRODUCTION_BASE = '/admin/'
const DEV_BASE = '/'

// 开发时 /api 与 /healthz 代理到本机 hub-server；生产由 Caddy 同域反代，前端不感知后端地址。
export default defineConfig(({ command }) => ({
  base: command === 'build' ? PRODUCTION_BASE : DEV_BASE,
  plugins: [react()],
  server: {
    port: DEV_PORT,
    strictPort: true,
    proxy: {
      '/api': HUB_SERVER_ORIGIN,
      '/healthz': HUB_SERVER_ORIGIN,
    },
  },
}))
