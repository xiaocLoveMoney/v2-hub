#!/usr/bin/env bash
# IT-4 派发与对账（docs/08，M2 DoD）：两台全新 3x-ui → 各接入一个入站 → 节点组 + 套餐 → 建用户后两台面板都出现 client
# → 已用达到额度后两台面板上的 client 被禁用 → 清零后恢复 → 删除用户后两台面板上的 client 消失。黑盒，只用 curl / jq / docker。
# 注意：会重建 xui-a、xui-b 容器（不挂数据卷，见 docker-compose.yml），不影响 PostgreSQL 与 MinIO。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
BASE=http://127.0.0.1:8080
ROOT_USER=it4-root  # 与 IT-2 / IT-3 的超管区分（run_all 只在开头重置一次库）
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-4: $*"; exit 1; }

# fresh_panel <服务名> <地址>：重建全新 3x-ui，按日志里的一次性账号登录并签发 admin 范围 token，打印 token
fresh_panel() {
  local svc="$1" url="$2" jar="$WORK/$1.jar" user="" pw="" logs
  (cd "$DEPLOY_DIR" && docker compose -f docker-compose.yml --profile xui up -d --force-recreate --renew-anon-volumes "$svc" >/dev/null 2>&1) || return 1
  for _ in $(seq 1 30); do
    logs="$(docker compose -f "$DEPLOY_DIR/docker-compose.yml" logs "$svc" 2>/dev/null)"
    user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
    pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
    [ -n "$user" ] && curl -s -o /dev/null "$url/csrf-token" && break
    sleep 1
  done
  [ -n "$user" ] || return 1
  local csrf body
  csrf="$(curl -s -b "$jar" -c "$jar" "$url/csrf-token" | jq -r .obj)"
  body="$(jq -nc --arg u "$user" --arg p "$pw" '{username:$u,password:$p}')"
  curl -s -b "$jar" -c "$jar" -X POST "$url/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" --data "$body" | jq -e .success >/dev/null || return 1
  csrf="$(curl -s -b "$jar" -c "$jar" "$url/csrf-token" | jq -r .obj)"
  curl -s -b "$jar" -c "$jar" -X POST "$url/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
    -H "X-CSRF-Token: $csrf" --data '{"name":"it4","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}

# ---- 1. 两台全新 3x-ui ----
URL_A="http://127.0.0.1:${XUI_A_PORT:-12053}"
URL_B="http://127.0.0.1:${XUI_B_PORT:-12054}"
TOKEN_A="$(fresh_panel xui-a "$URL_A")"; [ -n "$TOKEN_A" ] || fail "xui-a 签发 token 失败"
TOKEN_B="$(fresh_panel xui-b "$URL_B")"; [ -n "$TOKEN_B" ] || fail "xui-b 签发 token 失败"
# clients_of <地址> <token>：面板上的 client 列表（email、启用、挂载的入站）
clients_of() { curl -s -H "Authorization: Bearer $2" "$1/panel/api/clients/list" | jq -c '[.obj[] | {email, enable, inboundIds}]'; }

# ---- 2. hub-server 与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080
HUB_JAR="$WORK/hub.jar"
hub_login="$(jq -nc --arg u "$ROOT_USER" --arg p "$CRED" '{username:$u,password:$p}')"
CSRF="$(curl -s -b "$HUB_JAR" -c "$HUB_JAR" -X POST "$BASE/api/admin/auth/login" -H 'Content-Type: application/json' --data "$hub_login" | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
# hub <method> <path> [json] → 打印响应体；状态码写入 $WORK/code
hub() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$HUB_JAR" -c "$HUB_JAR" -X "$1" -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF")
  [ -n "${3:-}" ] && args+=(--data "$3")
  curl "${args[@]}" "$BASE$2" > "$WORK/code"
  cat "$WORK/body"
}
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }

# ---- 3. 两台服务器各接入一个入站，全部节点放进一个节点组 ----
add_server() {  # add_server <名称> <地址> <token> <端口> → 打印 "服务器id 面板入站id"
  local srv id inbound
  srv="$(hub POST /api/admin/servers "$(jq -nc --arg n "$1" --arg u "$2" --arg t "$3" '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')")"
  expect_code 200 "接入 $1"
  id="$(printf '%s' "$srv" | jq -r .data.id)"
  inbound="$(hub POST "/api/admin/servers/$id/inbounds" "$(jq -nc --argjson p "$4" '{remark:"it4-ws",port:$p,template:"vmess-ws",templateParams:{path:"/it4"}}')")"
  expect_code 200 "在 $1 新建入站"
  echo "$id $(printf '%s' "$inbound" | jq -r .data.remoteRef)"
}
read -r SERVER_A REF_A <<< "$(add_server it4-a "$URL_A" "$TOKEN_A" 24601)"
read -r SERVER_B REF_B <<< "$(add_server it4-b "$URL_B" "$TOKEN_B" 24602)"
node_ids="$(hub GET "/api/admin/nodes?pageSize=100" | jq -c '[.data.items[].id]')"
[ "$(printf '%s' "$node_ids" | jq length)" = "2" ] || fail "应有两个直连节点：$node_ids"
group="$(hub POST /api/admin/node-groups "$(jq -nc --argjson ids "$node_ids" '{name:"it4-group",nodeIds:$ids}')")"; expect_code 200 "新建节点组"
GROUP_ID="$(printf '%s' "$group" | jq -r .data.id)"

# ---- 4. 套餐 + 用户 → 两台面板都出现 client（docs/03 U1、§5.2 下发） ----
plan="$(hub POST /api/admin/plans "$(jq -nc --argjson g "$GROUP_ID" --argjson l "$GIB" '{name:"it4-plan",trafficLimit:$l,durationDays:30,nodeGroupIds:[$g]}')")"
expect_code 200 "新建套餐"
PLAN_ID="$(printf '%s' "$plan" | jq -r .data.id)"
user="$(hub POST /api/admin/users "$(jq -nc --arg p "$USER_CRED" --argjson plan "$PLAN_ID" '{username:"it4-user",password:$p,planId:$plan}')")"
expect_code 200 "新建用户"
USER_ID="$(printf '%s' "$user" | jq -r .data.id)"
[ "$(printf '%s' "$user" | jq -r .data.status)" = "active" ] || fail "新用户应为 active：$user"
hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null; expect_code 200 "对账"
provs="$(hub GET "/api/admin/users/$USER_ID/provisions")"
[ "$(printf '%s' "$provs" | jq '[.data[] | select(.status == "synced")] | length')" = "2" ] || fail "两台面板都应已同步：$provs"
EMAIL="$(printf '%s' "$provs" | jq -r '.data[0].email')"
for pair in "$URL_A $TOKEN_A $REF_A" "$URL_B $TOKEN_B $REF_B"; do
  read -r url tok ref <<< "$pair"
  clients_of "$url" "$tok" | jq -e --arg e "$EMAIL" --argjson r "$ref" 'any(.[]; .email == $e and .enable and (.inboundIds | index($r)))' >/dev/null \
    || fail "$url 上没有挂到入站 $ref 的启用 client $EMAIL：$(clients_of "$url" "$tok")"
done

# ---- 5. 已用达到额度 → limited → 两台面板禁用（U2、U7）；清零后恢复 ----
set_used() {  # set_used <字节数> → 打印修改后的用户
  local cur
  cur="$(hub GET "/api/admin/users/$USER_ID" | jq -c --argjson u "$1" '.data | {trafficLimit, trafficUsed: $u, expiresAt, pendingDays, limitIp, renewEveryDays, renewMonthDay, renewWeekday, renewMax, trafficReset, trafficResetDay}')"
  hub PUT "/api/admin/users/$USER_ID/quota" "$cur"
}
[ "$(set_used "$GIB" | jq -r .data.status)" = "limited" ] || fail "已用达到额度应为 limited：$(cat "$WORK/body")"
hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null; expect_code 200 "超额后对账"
for pair in "$URL_A $TOKEN_A" "$URL_B $TOKEN_B"; do
  read -r url tok <<< "$pair"
  clients_of "$url" "$tok" | jq -e --arg e "$EMAIL" 'any(.[]; .email == $e and (.enable | not))' >/dev/null || fail "$url 上的 client 应被禁用：$(clients_of "$url" "$tok")"
done
[ "$(set_used 0 | jq -r .data.status)" = "active" ] || fail "清零后应为 active：$(cat "$WORK/body")"
hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null; expect_code 200 "清零后对账"
clients_of "$URL_A" "$TOKEN_A" | jq -e --arg e "$EMAIL" 'any(.[]; .email == $e and .enable)' >/dev/null || fail "清零后 client 应恢复启用"

# ---- 6. 删除用户 → 两台面板上的 client 消失 ----
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
for pair in "$URL_A $TOKEN_A" "$URL_B $TOKEN_B"; do
  read -r url tok <<< "$pair"
  clients_of "$url" "$tok" | jq -e --arg e "$EMAIL" 'all(.[]; .email != $e)' >/dev/null || fail "$url 上的 client 应已删除：$(clients_of "$url" "$tok")"
done
hub GET "/api/admin/users/$USER_ID" >/dev/null; expect_code 404 "删除后查询用户"
echo "IT-4 PASS"
