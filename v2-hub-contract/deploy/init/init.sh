#!/usr/bin/env bash
# v2-hub-init 的入口（Ruling-109），可重复执行，完成即退出：
#   1. 迁移：记录在库内表 hub_deploy_migrations。空库执行 init.sql 并把全部迁移记为已执行；之后只执行表里没有的，按文件名顺序
#   2. 图片上传用的桶 uploads（匿名只读、不能列目录）与 hub-server 的只写账号（Ruling-89）
#   3. 两个前端发布到桶 web-admin / web-user（Ruling-6）
# 环境变量：
#   PGHOST / PGPORT / PGUSER / PGPASSWORD / PGDATABASE   psql 标准变量
#   MINIO_ENDPOINT（默认 http://minio:9000）/ MINIO_ROOT_USER / MINIO_ROOT_PASSWORD
#   HUB_UPLOAD_USER / HUB_UPLOAD_PASSWORD                 hub-server 上传图片用的账号
#   HUB_LEGACY_APPLIED_FILE（可选）                        老部署的迁移状态文件，只在记录表还不存在时导入一次（deploy_prod.sh 用）
# 不打印任何凭据。
set -euo pipefail

readonly SQL_DIR=/opt/v2-hub/sql
readonly WEB_DIR=/opt/v2-hub/web
readonly MIGRATION_TABLE=hub_deploy_migrations
readonly MIGRATION_NAME_PATTERN='^[0-9]{4}_[a-z0-9_]+\.sql$'
readonly UPLOAD_BUCKET=uploads
readonly UPLOAD_POLICY=hub-upload
readonly WAIT_RETRIES=60
readonly WAIT_INTERVAL_SECONDS=2
readonly WEB_APPS=("admin:web-admin" "user:web-user")
WORK_DIR="$(mktemp -d)"
readonly WORK_DIR
trap 'rm -rf "$WORK_DIR"' EXIT

log() { printf '[v2-hub-init %s] %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { log "失败：$*"; exit 1; }

: "${PGHOST:?缺 PGHOST}" "${PGUSER:?缺 PGUSER}" "${PGPASSWORD:?缺 PGPASSWORD}" "${PGDATABASE:?缺 PGDATABASE}"
: "${MINIO_ROOT_USER:?缺 MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD:?缺 MINIO_ROOT_PASSWORD}"
: "${HUB_UPLOAD_USER:?缺 HUB_UPLOAD_USER}" "${HUB_UPLOAD_PASSWORD:?缺 HUB_UPLOAD_PASSWORD}"
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio:9000}"

psql_quiet() { psql -X -q -v ON_ERROR_STOP=1 "$@"; }
psql_value() { psql -X -tA -v ON_ERROR_STOP=1 "$@"; }

wait_for() {
  local name="$1"
  shift
  local i
  for ((i = 1; i <= WAIT_RETRIES; i++)); do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    sleep "$WAIT_INTERVAL_SECONDS"
  done
  fail "等待 $name 超时"
}

# 迁移名一律经 psql 变量 :'name' 传入（按字面量引用），不拼进 SQL 文本
cat > "$WORK_DIR/create_table.sql" <<SQL
CREATE TABLE IF NOT EXISTS $MIGRATION_TABLE (
    name text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
SQL
cat > "$WORK_DIR/record.sql" <<SQL
INSERT INTO $MIGRATION_TABLE (name) VALUES (:'name') ON CONFLICT (name) DO NOTHING;
SQL
# 一次记入多条：names 是换行分隔的迁移名
cat > "$WORK_DIR/record_many.sql" <<SQL
INSERT INTO $MIGRATION_TABLE (name)
SELECT n FROM unnest(string_to_array(:'names', E'\\n')) AS n WHERE n <> ''
ON CONFLICT (name) DO NOTHING;
SQL
cat > "$WORK_DIR/applied.sql" <<SQL
SELECT count(*) FROM $MIGRATION_TABLE WHERE name = :'name';
SQL

# 镜像里的迁移名，换行分隔、按文件名排序；调用方用普通赋值接住，出错时 set -e 才生效
migration_names() {
  local file name
  for file in "$SQL_DIR"/migrations/*.sql; do
    name="$(basename "$file")"
    [[ "$name" =~ $MIGRATION_NAME_PATTERN ]] || fail "迁移文件名不合规：$name"
    printf '%s\n' "$name"
  done
}

# 先校验整份文件，再在一个事务里建表并记入，失败时库里不留半份记录
import_legacy_state() {
  local file="$1" name names=""
  log "导入旧迁移状态文件"
  while IFS= read -r name; do
    [ -z "$name" ] && continue
    [[ "$name" =~ $MIGRATION_NAME_PATTERN ]] || fail "旧状态文件里有不合规的迁移名：$name"
    names+="$name"$'\n'
  done < "$file"
  psql_quiet -1 -v names="$names" -f "$WORK_DIR/create_table.sql" -f "$WORK_DIR/record_many.sql"
}

migrate() {
  local table_exists public_tables name names
  names="$(migration_names)"
  table_exists="$(psql_value -c "SELECT to_regclass('public.$MIGRATION_TABLE') IS NOT NULL")"
  if [ "$table_exists" != "t" ]; then
    public_tables="$(psql_value -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")"
    if [ "$public_tables" = "0" ]; then
      # init.sql 已是最新结构：建表、记录表与全部迁移名在同一个事务里
      log "空库：执行 init.sql"
      psql_quiet -1 -v names="$names" -f "$SQL_DIR/init.sql" -f "$WORK_DIR/create_table.sql" \
        -f "$WORK_DIR/record_many.sql"
      return 0
    fi
    # 库里已有表却没有记录表：不知道哪些迁移执行过，重放会破坏数据
    [ -n "${HUB_LEGACY_APPLIED_FILE:-}" ] && [ -r "$HUB_LEGACY_APPLIED_FILE" ] \
      || fail "数据库非空但没有 ${MIGRATION_TABLE}：老部署请经 deploy_prod.sh 升级（导入旧状态文件），或换一个空库"
    import_legacy_state "$HUB_LEGACY_APPLIED_FILE"
  fi
  # 迁移文件自带事务（部分文件里有 BEGIN / COMMIT），这里不再包一层；执行成功才记入
  while IFS= read -r name; do
    if [ "$(psql_value -v name="$name" -f "$WORK_DIR/applied.sql")" != "0" ]; then
      continue
    fi
    log "执行迁移 $name"
    psql_quiet -f "$SQL_DIR/migrations/$name"
    psql_quiet -v name="$name" -f "$WORK_DIR/record.sql"
  done <<< "$names"
}

prepare_uploads() {
  mc mb --ignore-existing "hub/$UPLOAD_BUCKET" >/dev/null
  # 不用 mc 的 download 预设：它还放开了 ListBucket，能列出全部对象名
  cat > "$WORK_DIR/anonymous.json" <<JSON
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::$UPLOAD_BUCKET/*"]}]}
JSON
  mc anonymous set-json "$WORK_DIR/anonymous.json" "hub/$UPLOAD_BUCKET" >/dev/null
  cat > "$WORK_DIR/policy.json" <<JSON
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::$UPLOAD_BUCKET/*"]}]}
JSON
  mc admin policy create hub "$UPLOAD_POLICY" "$WORK_DIR/policy.json" >/dev/null
  # 账号与密码从标准输入给 mc，不出现在进程参数里；已存在时更新密码
  printf '%s\n%s\n' "$HUB_UPLOAD_USER" "$HUB_UPLOAD_PASSWORD" | mc admin user add hub >/dev/null
  local out
  # 已挂过时 mc 报 already in effect，视为成功
  if ! out="$(mc admin policy attach hub "$UPLOAD_POLICY" --user "$HUB_UPLOAD_USER" 2>&1)"; then
    grep -qi 'already' <<< "$out" || fail "挂上传策略：$out"
  fi
  log "桶 $UPLOAD_BUCKET 与上传账号已就绪"
}

publish_web() {
  local app module bucket
  for app in "${WEB_APPS[@]}"; do
    module="${app%%:*}"
    bucket="${app##*:}"
    [ -f "$WEB_DIR/$module/index.html" ] || fail "镜像里缺 $module 前端产物"
    mc mb --ignore-existing "hub/$bucket" >/dev/null
    mc anonymous set download "hub/$bucket" >/dev/null
    mc mirror --quiet --overwrite --remove "$WEB_DIR/$module" "hub/$bucket" >/dev/null
    log "前端 $module → 桶 $bucket"
  done
}

log "等待 PostgreSQL（${PGHOST}）"
wait_for PostgreSQL pg_isready -q
migrate
log "迁移完成"

# 凭据经环境变量交给 mc，不出现在进程参数里（.env 里的密码由 init_env.sh 生成，为十六进制，不含 URL 保留字符）
export MC_HOST_hub="${MINIO_ENDPOINT/:\/\//://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@}"
log "等待 MinIO（${MINIO_ENDPOINT}）"
wait_for MinIO mc ls hub
prepare_uploads
publish_web
log "完成"
