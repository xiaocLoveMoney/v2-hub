#!/usr/bin/env bash
# IT-3 面板接入（docs/08）：重建全新 3x-ui 容器并签发 API token → 经 hub-server 接入服务器（含探测与首次同步）
# → 按模板新建 VLESS+Reality 入站并确认面板侧已存在 → 加 Host 生成第二个节点 → 节点组 → 面板侧直接改备注触发漂移
# → 删除入站后面板侧也消失。黑盒，只用 curl / jq / docker。
# 注意：会重建 xui-a 容器（不挂数据卷，见 docker-compose.yml），不影响 PostgreSQL 与 MinIO。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
BASE=http://127.0.0.1:8080
ROOT_USER=it3-root  # 与 IT-2 的 it-root 区分（run_all 只在开头重置一次库）
CRED="it-$(openssl rand -hex 12)"
XUI_JAR="$(mktemp)"
HUB_JAR="$(mktemp)"
trap 'rm -f "$XUI_JAR" "$HUB_JAR"; stop_all' EXIT

fail() { echo "IT-3: $*"; exit 1; }

# ---- 1. 全新 3x-ui，按日志里的一次性账号登录并签发 admin 范围的 API token ----
(cd "$DEPLOY_DIR" && docker compose -f docker-compose.yml --profile xui up -d --force-recreate --renew-anon-volumes xui-a >/dev/null 2>&1) || fail "启动 xui-a 失败"
xui_user="" xui_pw=""
for _ in $(seq 1 30); do
  logs="$(docker compose -f "$DEPLOY_DIR/docker-compose.yml" logs xui-a 2>/dev/null)"
  xui_user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
  xui_pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
  [ -n "$xui_user" ] && curl -s -o /dev/null "$XUI_URL/csrf-token" && break
  sleep 1
done
[ -n "$xui_user" ] || fail "没有在 xui-a 日志里找到初始账号"
xui_csrf() { curl -s -b "$XUI_JAR" -c "$XUI_JAR" "$XUI_URL/csrf-token" | jq -r .obj; }
login_body="$(jq -nc --arg u "$xui_user" --arg p "$xui_pw" '{username:$u,password:$p}')"
curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $(xui_csrf)" --data "$login_body" | jq -e .success >/dev/null || fail "3x-ui 登录失败"
XUI_TOKEN="$(curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(xui_csrf)" --data '{"name":"it3","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty')"
[ -n "$XUI_TOKEN" ] || fail "签发 3x-ui API token 失败"
xui_api() { curl -s -H "Authorization: Bearer $XUI_TOKEN" "$@"; }

# ---- 2. hub-server 与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080
hub_login="$(jq -nc --arg u "$ROOT_USER" --arg p "$CRED" '{username:$u,password:$p}')"
CSRF="$(curl -s -b "$HUB_JAR" -c "$HUB_JAR" -X POST "$BASE/api/admin/auth/login" -H 'Content-Type: application/json' --data "$hub_login" | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
# hub <method> <path> [json] → 打印响应体；状态码写入 /tmp/it3-code
hub() {
  local args=(-s -o /tmp/it3-body -w '%{http_code}' -b "$HUB_JAR" -c "$HUB_JAR" -X "$1" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF")
  [ -n "${3:-}" ] && args+=(--data "$3")
  curl "${args[@]}" "$BASE$2" > /tmp/it3-code
  cat /tmp/it3-body
}
expect_code() { [ "$(cat /tmp/it3-code)" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat /tmp/it3-code) $(cat /tmp/it3-body)"; }

# ---- 3. 接入服务器：错误 token 应被拒且不保存，正确 token 接入并同步 ----
bad_server="$(jq -nc --arg u "$XUI_URL" '{name:"it-bad",providerType:"xui",baseUrl:$u,apiToken:"wrong",tlsMode:"verify"}')"
hub POST /api/admin/servers "$bad_server" >/dev/null; expect_code 502 "错误 token 接入"
server_body="$(jq -nc --arg u "$XUI_URL" --arg t "$XUI_TOKEN" '{name:"it-xui-a",providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify",region:"HK"}')"
server="$(hub POST /api/admin/servers "$server_body")"; expect_code 200 "接入服务器"
SERVER_ID="$(printf '%s' "$server" | jq -r .data.id)"
[ "$(printf '%s' "$server" | jq -r .data.status)" = "online" ] || fail "接入后状态不是 online：$server"
printf '%s' "$server" | grep -q "$XUI_TOKEN" && fail "响应里不应出现 API token"

# ---- 4. 按模板建 VLESS+Reality 入站，面板侧确认存在且私钥未出现在平台响应 ----
inbound_body='{"remark":"it3-reality","port":24443,"template":"vless-reality","templateParams":{"target":"www.cloudflare.com:443"}}'
inbound="$(hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$inbound_body")"; expect_code 200 "新建入站"
INBOUND_ID="$(printf '%s' "$inbound" | jq -r .data.id)"
REMOTE_REF="$(printf '%s' "$inbound" | jq -r .data.remoteRef)"
printf '%s' "$inbound" | jq -e '.data.config.streamSettings.realitySettings.privateKey == null' >/dev/null || fail "平台响应泄露了 Reality 私钥"
xui_api "$XUI_URL/panel/api/inbounds/get/$REMOTE_REF" | jq -e '.obj.streamSettings.realitySettings.privateKey | length > 0' >/dev/null || fail "面板上没有新入站或缺少私钥"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$inbound_body" >/dev/null; expect_code 400 "端口冲突"

# ---- 5. Host → 第二个节点；节点组 ----
hub POST "/api/admin/servers/$SERVER_ID/endpoints" "$(jq -nc --argjson id "$INBOUND_ID" '{inboundIds:[$id],addresses:["it3.example.com"],port:8443,remark:"it3"}')" >/dev/null
expect_code 200 "添加 Host"
nodes="$(hub GET "/api/admin/nodes?serverId=$SERVER_ID")"; expect_code 200 "节点目录"
[ "$(printf '%s' "$nodes" | jq -r .data.total)" = "2" ] || fail "应有直连 + Host 两个节点：$nodes"
node_ids="$(printf '%s' "$nodes" | jq -c '[.data.items[].id]')"
hub POST /api/admin/node-groups "$(jq -nc --argjson ids "$node_ids" '{name:"it3-group",nodeIds:$ids}')" >/dev/null; expect_code 200 "新建节点组"

# ---- 6. 面板侧直接改备注 → 同步报告漂移 ----
current="$(xui_api "$XUI_URL/panel/api/inbounds/get/$REMOTE_REF" | jq '.obj | .remark = "changed-on-panel"')"
xui_api -X POST -H 'Content-Type: application/json' "$XUI_URL/panel/api/inbounds/update/$REMOTE_REF" --data "$current" | jq -e .success >/dev/null || fail "面板侧改备注失败"
sync="$(hub POST "/api/admin/servers/$SERVER_ID/sync")"; expect_code 200 "手动同步"
[ "$(printf '%s' "$sync" | jq -r .data.drifted)" = "1" ] || fail "应检测到 1 个漂移：$sync"
hub POST "/api/admin/inbounds/$INBOUND_ID/drift/ack" >/dev/null; expect_code 200 "确认漂移"

# ---- 7. 删除入站：面板侧也消失，节点随之删除 ----
hub DELETE "/api/admin/inbounds/$INBOUND_ID" >/dev/null; expect_code 200 "删除入站"
xui_api "$XUI_URL/panel/api/inbounds/list" | jq -e --arg r "$REMOTE_REF" '[.obj[] | select((.id|tostring) == $r)] | length == 0' >/dev/null || fail "面板上的入站没有被删除"
[ "$(hub GET "/api/admin/nodes?serverId=$SERVER_ID" | jq -r .data.total)" = "0" ] || fail "删除入站后节点应清空"
echo "IT-3 PASS"
