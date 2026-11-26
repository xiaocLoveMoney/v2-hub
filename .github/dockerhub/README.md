# v2-hub

Multi-server [3x-ui](https://github.com/MHSanaei/3x-ui) node aggregation and subscription platform — aggregate inbounds from many panels,
hand them out to users by plan, serve one subscription URL (Base64 / Mihomo / sing-box / Xray JSON), with client-side and server-side proxy chains.
Control plane only: no proxy traffic passes through v2-hub.

多服务器 3x-ui 节点聚合与订阅分发平台：聚合各面板的入站，按套餐派发给用户，提供统一订阅与链式代理；只做控制面，不转发代理流量。

- Source & full guide / 源码与完整部署教程: https://github.com/xiaocLoveMoney/v2-hub
- License: MIT

## Images / 镜像

| Image | Purpose |
|---|---|
| `xiaoclux/v2-hub-server` | Backend API, subscription endpoint, panel sync (distroless, non-root) |
| `xiaoclux/v2-hub-init` | One-shot init: DB migrations, upload bucket, publishes the admin & user web UIs to MinIO, then exits |

Also on GHCR: `ghcr.io/xiaoclovemoney/v2-hub-server`, `ghcr.io/xiaoclovemoney/v2-hub-init`. Platforms: `linux/amd64`, `linux/arm64`.

Tags: `latest` (newest release), `vX.Y` (release), `edge` (main), `sha-<commit>`. Always run both images with the same tag.

## Quick start / 快速开始

```bash
sudo mkdir -p /opt/v2-hub/caddy && cd /opt/v2-hub
BASE=https://raw.githubusercontent.com/xiaocLoveMoney/v2-hub/main/v2-hub-contract/deploy
sudo curl -fsSLo compose.yaml       $BASE/compose.deploy.yml
sudo curl -fsSLo caddy/Caddyfile    $BASE/caddy/Caddyfile
sudo curl -fsSLo deploy.env.example $BASE/deploy.env.example
sudo curl -fsSLo init_env.sh        $BASE/init_env.sh
sudo bash init_env.sh deploy.env.example .env   # random passwords + encryption key
sudo vi .env                                    # set HUB_DOMAIN
sudo docker compose up -d
sudo docker compose run --rm --no-deps -it --entrypoint /app/hub hub-server \
  admin-create -f /app/etc/hub-server.yaml -username admin
```

Admin UI: `https://<HUB_DOMAIN>/admin/` · User portal: `https://<HUB_DOMAIN>/`

Back up `.env` — `HUB_ENCRYPTION_KEY` encrypts secrets stored in the database and cannot be recovered.
