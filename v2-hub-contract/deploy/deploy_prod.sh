#!/usr/bin/env bash
# 生产部署（Ruling-62，docs/02 §6）：由生产机上的自托管 Runner 在仓库检出目录里执行，也可在生产机上手动执行。
#   bash v2-hub-contract/deploy/deploy_prod.sh
# 步骤：同步编排文件到 DEPLOY_ROOT → 本地构建 hub-server 镜像与两个前端 → 起数据库与 MinIO → 上传用的桶与账号 → 迁移
#       → compose 更新 → 发布前端 → 健康检查 → 清理旧镜像。
# 凭据只在 DEPLOY_ROOT/.env（首次装机时生成，不入库）；本脚本不打印任何凭据。
set -euo pipefail

DEPLOY_ROOT="${DEPLOY_ROOT:-/opt/v2-hub}"
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CONTRACT_DEPLOY="$REPO_ROOT/v2-hub-contract/deploy"
STATE_DIR="$DEPLOY_ROOT/state"
APPLIED_FILE="$STATE_DIR/applied-migrations"
SERVER_IMAGE="v2-hub-server"
CURRENT_TAG="current"
KEEP_IMAGES=3
HEALTH_RETRIES=30
HEALTH_INTERVAL_SECONDS=2
WEB_APPS=("admin-web:web-admin" "user-web:web-user")

log() { printf '[deploy %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { log "失败：$*"; exit 1; }

[ -f "$DEPLOY_ROOT/.env" ] || fail "缺 $DEPLOY_ROOT/.env（首次装机见 docs/02 §6）"
mkdir -p "$STATE_DIR"

# 同一时间只跑一个部署
exec 9>"$STATE_DIR/deploy.lock"
flock -n 9 || fail "已有部署在进行"

IMAGE_TAG="$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)"
export IMAGE_TAG
compose() { docker compose -f "$DEPLOY_ROOT/compose.deploy.yml" --env-file "$DEPLOY_ROOT/.env" "$@"; }

# 1. 同步编排文件。Caddyfile 以单文件挂进容器，用 cp 原地覆盖（不换 inode），容器内才能看到新内容
log "同步编排文件到 $DEPLOY_ROOT（提交 $IMAGE_TAG）"
mkdir -p "$DEPLOY_ROOT/caddy" "$DEPLOY_ROOT/migrations"
# publish_web.sh 读它所在目录的 .env，所以也放到 DEPLOY_ROOT
cp "$CONTRACT_DEPLOY/compose.deploy.yml" "$CONTRACT_DEPLOY/publish_web.sh" "$CONTRACT_DEPLOY/init_uploads.sh" \
  "$CONTRACT_DEPLOY/init.sql" "$DEPLOY_ROOT/"
cp "$CONTRACT_DEPLOY/caddy/Caddyfile" "$DEPLOY_ROOT/caddy/Caddyfile"
rm -f "$DEPLOY_ROOT"/migrations/*.sql
cp "$CONTRACT_DEPLOY"/migrations/*.sql "$DEPLOY_ROOT/migrations/"

# 2. 构建：后端镜像，前端只取 dist（前端不跑容器，Ruling-6）
log "构建 $SERVER_IMAGE:$IMAGE_TAG"
docker build --pull -t "$SERVER_IMAGE:$IMAGE_TAG" "$REPO_ROOT/v2-hub-server"
WEB_OUT="$(mktemp -d)"
trap 'rm -rf "$WEB_OUT"' EXIT
for app in "${WEB_APPS[@]}"; do
  module="${app%%:*}"
  log "构建前端 $module"
  docker build --target dist --output "type=local,dest=$WEB_OUT/$module" "$REPO_ROOT/v2-hub-$module"
done

# 3. 数据库与 MinIO 先起，迁移要用
log "启动 postgres / minio"
compose up -d --wait postgres minio

# 图片上传用的 MinIO 桶与只写账号（Ruling-89）；老环境的 .env 缺账号时由脚本补上
bash "$DEPLOY_ROOT/init_uploads.sh" "$DEPLOY_ROOT/compose.deploy.yml"

# 单引号里的变量由 postgres 容器内的 shell 展开（容器环境里有 POSTGRES_USER / POSTGRES_DB）
# shellcheck disable=SC2016
psql_exec() {
  compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -q -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
}

# 4. 迁移：空库执行 init.sql 并把现有迁移全部记为已执行；之后只执行状态文件里没有的
# shellcheck disable=SC2016
table_count="$(compose exec -T postgres sh -c \
  'psql -tA -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = '\''public'\''"')"
if [ "$table_count" = "0" ]; then
  log "空库：执行 init.sql"
  psql_exec < "$DEPLOY_ROOT/init.sql"
  : > "$APPLIED_FILE"
  for file in "$DEPLOY_ROOT"/migrations/*.sql; do
    basename "$file" >> "$APPLIED_FILE"
  done
else
  touch "$APPLIED_FILE"
  for file in "$DEPLOY_ROOT"/migrations/*.sql; do
    name="$(basename "$file")"
    if grep -qxF "$name" "$APPLIED_FILE"; then
      continue
    fi
    log "执行迁移 $name"
    psql_exec < "$file"
    echo "$name" >> "$APPLIED_FILE"
  done
fi

# 5. 全部服务更新到新镜像；Caddyfile 有变化时容器不会重建，显式重载
log "更新服务"
compose up -d --remove-orphans
compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile

# 6. 发布前端到 MinIO
for app in "${WEB_APPS[@]}"; do
  module="${app%%:*}"
  bucket="${app##*:}"
  bash "$DEPLOY_ROOT/publish_web.sh" "$WEB_OUT/$module" "$bucket" "$DEPLOY_ROOT/compose.deploy.yml"
done

# 7. 健康检查：经 Caddy 容器访问 hub-server（distroless 镜像里没有 shell / curl）
log "健康检查"
for ((i = 1; i <= HEALTH_RETRIES; i++)); do
  if compose exec -T caddy wget -q -O /dev/null http://hub-server:8080/healthz; then
    log "hub-server 健康"
    break
  fi
  if [ "$i" -eq "$HEALTH_RETRIES" ]; then
    compose logs --tail 50 hub-server
    fail "hub-server 健康检查超时；旧镜像仍在，可 docker tag 回 $SERVER_IMAGE:$CURRENT_TAG 后 compose up -d"
  fi
  sleep "$HEALTH_INTERVAL_SECONDS"
done

# 8. 健康后才把 current 指向本次镜像；只保留最近几个版本
docker tag "$SERVER_IMAGE:$IMAGE_TAG" "$SERVER_IMAGE:$CURRENT_TAG"
docker images "$SERVER_IMAGE" --format '{{.CreatedAt}}\t{{.Tag}}' | sort -r | cut -f2 \
  | grep -vxF "$CURRENT_TAG" | tail -n +$((KEEP_IMAGES + 1)) \
  | xargs -r -I{} docker rmi "$SERVER_IMAGE:{}" >/dev/null
docker image prune -f >/dev/null
docker builder prune -f --filter until=168h >/dev/null
log "完成：$SERVER_IMAGE:$IMAGE_TAG"
