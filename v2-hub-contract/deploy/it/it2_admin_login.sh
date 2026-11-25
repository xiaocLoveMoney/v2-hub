#!/usr/bin/env bash
# IT-2 管理员登录（docs/08）：admin-create 建超管 → 登录拿 Cookie 与 CSRF → me → 无 CSRF 的写请求 403
# → 绑定两步验证（超管必须开启，R10）→ 建只读角色与管理员 → 低权限访问管理员列表 403 → 登出后 401。
# 黑盒，只用 curl（两步验证的辅助见 common.sh，另用 jq / python3）。
# 注意：含逗号的 JSON 一律先放进变量再传参——macOS 自带 bash 3.2 会对 "$(… "{a,b}")" 做花括号展开。
source "$(dirname "$0")/common.sh"
set -e

BASE=http://127.0.0.1:8080
ROOT_USER=it-root
VIEWER_USER=it-viewer
CRED="it-$(openssl rand -hex 12)"
JAR_ROOT="$(mktemp)"
JAR_VIEWER="$(mktemp)"
HUB_CONFIG="$(mktemp)"
trap 'rm -f "$JAR_ROOT" "$JAR_VIEWER" "$HUB_CONFIG"; stop_all' EXIT

printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 \
  || { echo "IT-2: admin-create 失败，见 /tmp/it-admin-create.log"; exit 1; }
it_hub_config "$HUB_CONFIG" || { echo "IT-2: 派生配置失败"; exit 1; }
start_module v2-hub-server 8080 CONFIG="$HUB_CONFIG"

# api <jar> <method> <path> [json] [csrf] → 打印 "状态码 响应体"
api() {
  local jar="$1" method="$2" path="$3" body="${4:-}" csrf="${5:-}"
  local args=(-s -o /tmp/it2-body -w '%{http_code}' -b "$jar" -c "$jar" -X "$method" -H 'Content-Type: application/json')
  [ -n "$body" ] && args+=(--data "$body")
  [ -n "$csrf" ] && args+=(-H "X-CSRF-Token: $csrf")
  local code
  code="$(curl "${args[@]}" "$BASE$path")"
  printf '%s %s' "$code" "$(cat /tmp/it2-body)"
}
expect() {  # expect <期望状态码> <实际输出> <说明>
  case "$2" in
    "$1 "*) ;;
    *) echo "IT-2: $3 期望 $1，实际 $2"; exit 1 ;;
  esac
}
csrf_of() { sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p'; }
login_body() { printf '{"username":"%s","password":"%s"}' "$1" "$2"; }

ROLE_BODY='{"code":"viewer","name":"只读","permissions":["node:read"]}'
VIEWER_BODY="$(printf '{"username":"%s","password":"%s","roles":["viewer"]}' "$VIEWER_USER" "$CRED")"
WRONG_LOGIN="$(login_body "$ROOT_USER" "wrong-$CRED")"
ROOT_LOGIN="$(login_body "$ROOT_USER" "$CRED")"
VIEWER_LOGIN="$(login_body "$VIEWER_USER" "$CRED")"

out="$(api "$JAR_ROOT" GET /api/admin/auth/me)";                    expect 401 "$out" "未登录访问 me"
out="$(api "$JAR_ROOT" POST /api/admin/auth/login "$WRONG_LOGIN")"; expect 401 "$out" "错误密码登录"
out="$(api "$JAR_ROOT" POST /api/admin/auth/login "$ROOT_LOGIN")";  expect 200 "$out" "超管登录"
CSRF="$(printf '%s' "$out" | csrf_of)"
[ -n "$CSRF" ] || { echo "IT-2: 登录响应缺 csrfToken"; exit 1; }

out="$(api "$JAR_ROOT" GET /api/admin/auth/me)";                         expect 200 "$out" "登录后 me"
out="$(api "$JAR_ROOT" POST /api/admin/roles "$ROLE_BODY")";             expect 403 "$out" "缺 CSRF 的写请求"
# 新建的超管还没开两步验证，先绑定才能调业务接口（R10，未绑定时的拦截与豁免由 IT-17 覆盖）
it_admin_totp_bind "$JAR_ROOT" "$CSRF" "$ROOT_USER" || { echo "IT-2: 绑定两步验证失败"; exit 1; }
out="$(api "$JAR_ROOT" POST /api/admin/roles "$ROLE_BODY" "$CSRF")";     expect 200 "$out" "建只读角色"
out="$(api "$JAR_ROOT" POST /api/admin/admins "$VIEWER_BODY" "$CSRF")";  expect 200 "$out" "建只读管理员"

out="$(api "$JAR_VIEWER" POST /api/admin/auth/login "$VIEWER_LOGIN")";   expect 200 "$out" "只读管理员登录"
out="$(api "$JAR_VIEWER" GET /api/admin/admins)";                        expect 403 "$out" "只读管理员访问管理员列表"

out="$(api "$JAR_ROOT" GET '/api/admin/audit-logs?pageSize=20')";        expect 200 "$out" "审计日志"
out="$(api "$JAR_ROOT" POST /api/admin/auth/logout '' "$CSRF")";         expect 200 "$out" "登出"
out="$(api "$JAR_ROOT" GET /api/admin/auth/me)";                         expect 401 "$out" "登出后 me"
echo "IT-2 PASS"
