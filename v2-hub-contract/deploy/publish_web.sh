#!/usr/bin/env bash
# 把前端构建产物发布到 MinIO 桶（Ruling-6）。用法：
#   publish_web.sh <dist 目录> <桶名: web-admin | web-user> [compose 文件，默认 docker-compose.yml]
# 桶不存在则创建并设为匿名只读（只放静态文件，不放任何用户数据）；发布用 mirror --remove 保持与 dist 一致。
set -euo pipefail
DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
DIST_DIR="$(cd "${1:?用法: publish_web.sh <dist 目录> <桶名> [compose 文件]}" && pwd)"
BUCKET="${2:?缺桶名}"
COMPOSE_FILE="${3:-docker-compose.yml}"
MC_IMAGE="pgsty/mc:latest"

case "$BUCKET" in
  web-admin|web-user) ;;
  *) echo "publish_web: 桶名只允许 web-admin / web-user"; exit 1 ;;
esac
[ -f "$DIST_DIR/index.html" ] || { echo "publish_web: $DIST_DIR 下没有 index.html，先 make build"; exit 1; }

cd "$DEPLOY_DIR"
set -a && . ./.env && set +a
: "${MINIO_ROOT_USER:?}" "${MINIO_ROOT_PASSWORD:?}"

minio_container="$(docker compose -f "$COMPOSE_FILE" ps -q minio)"
[ -n "$minio_container" ] || { echo "publish_web: minio 容器未运行"; exit 1; }

# 口令经环境变量传给容器（-e 只写变量名），不出现在命令行参数里。
export MC_HOST_hub="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@127.0.0.1:9000"
docker run --rm --network "container:$minio_container" -e MC_HOST_hub \
  -v "$DIST_DIR:/dist:ro" --entrypoint sh "$MC_IMAGE" -c "
    set -e
    mc mb --ignore-existing hub/$BUCKET
    mc anonymous set download hub/$BUCKET
    mc mirror --overwrite --remove /dist hub/$BUCKET
  "
echo "publish_web: $DIST_DIR → $BUCKET 完成"
