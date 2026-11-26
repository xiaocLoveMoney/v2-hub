#!/usr/bin/env bash
# 由模板生成 .env，并为留空的 *_PASSWORD / *_KEY 填入随机值。输出文件已存在时不覆盖。用法：
#   bash init_env.sh                          # 开发：本目录 .env.example → 本目录 .env
#   bash init_env.sh deploy.env.example .env  # 部署（Ruling-109）：路径相对当前目录
set -euo pipefail
if [ "$#" -eq 0 ]; then
  cd "$(dirname "$0")"
fi
template="${1:-.env.example}"
output="${2:-.env}"
[ -f "$template" ] || { echo "init_env: 找不到模板 $template"; exit 1; }
if [ -f "$output" ]; then
  echo "init_env: $output 已存在，未改动"
  exit 0
fi
umask 077
while IFS= read -r line; do
  case "$line" in
    \#*)
      printf '%s\n' "$line" ;;
    *_PASSWORD=)
      printf '%s%s\n' "$line" "$(openssl rand -hex 16)" ;;
    *_KEY=)
      printf '%s%s\n' "$line" "$(openssl rand -base64 32)" ;;
    *)
      printf '%s\n' "$line" ;;
  esac
done < "$template" > "$output"
echo "init_env: 已生成 ${output}（密码为随机值，仅本机可读）"
