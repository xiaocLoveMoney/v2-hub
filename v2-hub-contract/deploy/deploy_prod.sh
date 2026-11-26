#!/usr/bin/env bash
# 生产部署（Ruling-62、Ruling-109，docs/02 §6）：由生产机上的自托管 Runner 在仓库检出目录里执行，也可在生产机上手动执行。
#   bash v2-hub-contract/deploy/deploy_prod.sh
# 镜像由工作流 images 在 GitHub 托管机上构建并推到 GHCR，这里只按提交号拉取，不在生产机上编译。
# 步骤：同步编排文件到 DEPLOY_ROOT → .env 补上传账号 → 写 image.env → 拉镜像 →（仅首次升级）导入旧迁移状态
#       → compose 更新（init 做迁移、上传桶、发布前端，成功后更新 hub-server）→ 重载 Caddy → 健康检查 → 清理旧镜像。
# 可选环境变量：HUB_IMAGE_REGISTRY（默认 GHCR）、HUB_IMAGE_TAG（默认 sha-<当前检出的完整提交号>）。
# 凭据只在 DEPLOY_ROOT/.env（首次装机时生成，不入库）；本脚本不打印任何凭据。
set -euo pipefail

DEPLOY_ROOT="${DEPLOY_ROOT:-/opt/v2-hub}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CONTRACT_DEPLOY="$REPO_ROOT/v2-hub-contract/deploy"
STATE_DIR="$DEPLOY_ROOT/state"
LEGACY_APPLIED_FILE="$STATE_DIR/applied-migrations"
IMAGE_ENV_FILE="$DEPLOY_ROOT/image.env"
IMAGE_REGISTRY="${HUB_IMAGE_REGISTRY:-ghcr.io/xiaoclovemoney}"
IMAGE_TAG="${HUB_IMAGE_TAG:-sha-$(git -C "$REPO_ROOT" rev-parse HEAD)}"
IMAGE_NAMES=("v2-hub-server" "v2-hub-init")
SHA_TAG_PREFIX="sha-"
KEEP_IMAGES=3
HEALTH_RETRIES=30
HEALTH_INTERVAL_SECONDS=2
# 老部署方式留在 DEPLOY_ROOT 的文件，迁移与前端发布已移进 init 镜像
LEGACY_FILES=("init.sql" "publish_web.sh" "init_uploads.sh")

log() { printf '[deploy %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { log "失败：$*"; exit 1; }

[ -f "$DEPLOY_ROOT/.env" ] || fail "缺 $DEPLOY_ROOT/.env（首次装机见 docs/02 §6）"
mkdir -p "$STATE_DIR"

# 同一时间只跑一个部署
exec 9>"$STATE_DIR/deploy.lock"
flock -n 9 || fail "已有部署在进行"

compose() {
  docker compose -f "$DEPLOY_ROOT/compose.deploy.yml" --env-file "$DEPLOY_ROOT/.env" --env-file "$IMAGE_ENV_FILE" "$@"
}

# 1. 同步编排文件。Caddyfile 以单文件挂进容器，用 cp 原地覆盖（不换 inode），容器内才能看到新内容
log "同步编排文件到 ${DEPLOY_ROOT}（镜像 ${IMAGE_REGISTRY}/*:${IMAGE_TAG}）"
mkdir -p "$DEPLOY_ROOT/caddy"
cp "$CONTRACT_DEPLOY/compose.deploy.yml" "$DEPLOY_ROOT/"
cp "$CONTRACT_DEPLOY/caddy/Caddyfile" "$DEPLOY_ROOT/caddy/Caddyfile"
for file in "${LEGACY_FILES[@]}"; do
  rm -f "${DEPLOY_ROOT:?}/$file"
done
rm -rf "${DEPLOY_ROOT:?}/migrations"

# 2. 上传账号（Ruling-89）：老环境的 .env 缺密码时补上。只追加、不重写——.env 里有加密主密钥，重写中途失败会把它截掉
if ! grep -q '^HUB_UPLOAD_PASSWORD=.' "$DEPLOY_ROOT/.env"; then
  if [ -n "$(tail -c 1 "$DEPLOY_ROOT/.env")" ]; then
    echo >> "$DEPLOY_ROOT/.env"
  fi
  printf 'HUB_UPLOAD_USER=hub-upload\nHUB_UPLOAD_PASSWORD=%s\n' "$(openssl rand -hex 16)" >> "$DEPLOY_ROOT/.env"
  log "已在 .env 里追加上传账号"
fi

# 3. 本次的镜像写进 image.env，手动执行 compose 时带上它（docs/02 §6 手动操作）
printf 'HUB_IMAGE_REGISTRY=%s\nHUB_IMAGE_TAG=%s\n' "$IMAGE_REGISTRY" "$IMAGE_TAG" > "$IMAGE_ENV_FILE.tmp"
mv "$IMAGE_ENV_FILE.tmp" "$IMAGE_ENV_FILE"

# 4. 拉镜像（含 Caddy / PostgreSQL / MinIO 的固定版本）
log "拉取镜像"
compose pull --quiet || fail "拉取镜像失败：确认工作流 images 已为该提交推送 $IMAGE_TAG"

# 5. 首次升级到按镜像部署：把旧状态文件导入库内的迁移记录表，之后状态文件不再使用
if [ -f "$LEGACY_APPLIED_FILE" ]; then
  log "导入旧迁移状态 $LEGACY_APPLIED_FILE"
  compose run --rm -v "$LEGACY_APPLIED_FILE:/legacy/applied-migrations:ro" \
    -e HUB_LEGACY_APPLIED_FILE=/legacy/applied-migrations init || fail "init 导入旧迁移状态失败"
  mv "$LEGACY_APPLIED_FILE" "$LEGACY_APPLIED_FILE.imported"
fi

# 6. 全部服务更新到新镜像：init 先跑完（迁移、上传桶、发布前端），hub-server 才更新；Caddyfile 有变化时容器不会重建，显式重载
log "更新服务"
if ! compose up -d --remove-orphans; then
  compose logs --tail 80 init hub-server
  fail "compose up 失败"
fi
compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile

# 7. 健康检查：经 Caddy 容器访问 hub-server（distroless 镜像里没有 shell / curl）
log "健康检查"
for ((i = 1; i <= HEALTH_RETRIES; i++)); do
  if compose exec -T caddy wget -q -O /dev/null http://hub-server:8080/healthz; then
    log "hub-server 健康"
    break
  fi
  if [ "$i" -eq "$HEALTH_RETRIES" ]; then
    compose logs --tail 50 hub-server
    fail "hub-server 健康检查超时；回滚：把 $IMAGE_ENV_FILE 的 HUB_IMAGE_TAG 改回上一个 ${SHA_TAG_PREFIX}… 后 compose up -d"
  fi
  sleep "$HEALTH_INTERVAL_SECONDS"
done

# 8. 每个镜像只保留最近几个 sha- 标签（含本次），便于回滚；清理失败不算部署失败
for name in "${IMAGE_NAMES[@]}"; do
  docker images "$IMAGE_REGISTRY/$name" --format '{{.CreatedAt}}\t{{.Tag}}' | sort -r | cut -f2 \
    | { grep "^$SHA_TAG_PREFIX" || true; } | { grep -vxF "$IMAGE_TAG" || true; } | tail -n +"$KEEP_IMAGES" \
    | xargs -r -I{} docker rmi "$IMAGE_REGISTRY/$name:{}" >/dev/null || log "清理 $name 旧镜像失败，跳过"
done
docker image prune -f >/dev/null
log "完成：$IMAGE_REGISTRY/*:$IMAGE_TAG"
