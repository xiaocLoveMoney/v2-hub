#!/usr/bin/env bash
# 把数据库重置到 init.sql 初始态（幂等）。经 compose 里的 postgres 容器执行，本机无需装 psql。
# 联调脚本 run_all.sh 也调它。注意：会清空数据。
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] || { echo "reset_db: 缺 deploy/.env，先 cp .env.example .env"; exit 1; }
set -a && . ./.env && set +a
: "${POSTGRES_USER:?}" "${POSTGRES_DB:?}"

psql_exec() {
  docker compose -f docker-compose.yml exec -T postgres psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$@"
}

psql_exec -d postgres -c "DROP DATABASE IF EXISTS \"$POSTGRES_DB\" WITH (FORCE);"
psql_exec -d postgres -c "CREATE DATABASE \"$POSTGRES_DB\" ENCODING 'UTF8';"
psql_exec -d "$POSTGRES_DB" < init.sql
echo "reset_db: $POSTGRES_DB 已重置到 init.sql"
