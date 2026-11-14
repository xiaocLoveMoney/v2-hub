#!/usr/bin/env bash
# 由 .env.example 生成 .env，并为留空的密码项填入随机值。已存在 .env 时不覆盖。
set -euo pipefail
cd "$(dirname "$0")"
if [ -f .env ]; then
  echo "init_env: .env 已存在，未改动"
  exit 0
fi
umask 077
while IFS= read -r line; do
  case "$line" in
    *_PASSWORD=)
      printf '%s%s\n' "$line" "$(openssl rand -hex 16)" ;;
    *_KEY=)
      printf '%s%s\n' "$line" "$(openssl rand -base64 32)" ;;
    *)
      printf '%s\n' "$line" ;;
  esac
done < .env.example > .env
echo "init_env: 已生成 deploy/.env（密码为随机值，仅本机可读）"
