import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const DEV_PORT = 5174
const HUB_SERVER_ORIGIN = 'http://127.0.0.1:8080'
/** 本机开发的 MinIO（contract/deploy/docker-compose.yml）；公告 / 教程里的图片在桶 uploads（Ruling-89）。 */
const MINIO_ORIGIN = 'http://127.0.0.1:19000'
const SUBSCRIPTION_PATH = '^/[a-z0-9]{10,32}/[A-Za-z0-9_-]{22,64}(\\?.*)?$'

// 开发时 /api、订阅地址与 /healthz 代理到本机 hub-server，/uploads 代理到本机 MinIO；生产由 Caddy 同域反代，前端不感知后端地址。
export default defineConfig({
  plugins: [react()],
  server: {
    port: DEV_PORT,
    strictPort: true,
    proxy: {
      '/api': HUB_SERVER_ORIGIN,
      '/sub': HUB_SERVER_ORIGIN,
      // 订阅地址的随机前缀（Ruling-59）：与生产 Caddy 同一形状——10–32 位小写字母数字 + 令牌，可带 ?format=
      [SUBSCRIPTION_PATH]: HUB_SERVER_ORIGIN,
      '/healthz': HUB_SERVER_ORIGIN,
      '/uploads': MINIO_ORIGIN,
    },
  },
})
