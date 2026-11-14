#!/usr/bin/env bash
# IT-11 链式规则与出口（docs/08，M9 DoD，docs/03 §5.18、§5.19、§5.21）：全新 xui-a、xui-b 各一个 VMess+WS 入站
# → 平台出口挂节点组前用户看不到、挂上后可见且不返回链接 → “两个起飞节点 → 平台出口”的服务端规则下发后两台面板各有一组 hub-r{id} 条目，
#   路由按本人 email 匹配 → 再加一条落地为我的出口的规则，被第 1 条覆盖；调顺序后生效的规则随之变化
# → 我的出口填回环地址 / localhost 用于服务端链式 400、用于客户端链式允许；被服务端规则引用时换成回环地址 400 → 反向规则成环 400
# → 管理员停用用户出口后规则阻断、靠后的规则顶上；管理员看不了用户出口的链接 → 他人的规则与出口 404
# → 平台出口摘出节点组后规则阻断、两台面板的条目都撤下 → 改为指定用户后按名单授权（Ruling-64）→ 客户端规则按（起飞，落地）组合进订阅 → 连续改动触发 429。
# 黑盒，只用 curl / jq / docker。用户出口的地址只写进面板配置，没有流量经过，平台与面板都不会去连它。
# 注意：会重建 xui-a、xui-b 两个容器（不挂数据卷），不影响 PostgreSQL 与 MinIO。hub-server 用缩短了下发间隔的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_A_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
XUI_B_URL="http://127.0.0.1:${XUI_B_PORT:-12054}"
ROOT_USER=it11-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it11-user
OTHER_USER=it11-other
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
OTHER_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
PORT_A=25101
PORT_B=25102
APPLY_WAIT_SECONDS=60
WRITE_LIMIT=40               # 临时配置里每用户的服务端规则写操作上限：前面的步骤用不完，最后专门打满
PLATFORM_LINK="socks5://xui-b:1080#it11-platform"   # 平台出口不受地址限制（Ruling-48）
PLATFORM_NAME=it11-平台出口
OWN_PUBLIC_LINK="socks5://9.9.9.9:1080#it11-own"    # 公网地址；没有流量经过，不会真的去连
OWN_LOOP_LINK="socks5://127.0.0.1:1080#it11-loop"
OWN_LOCALHOST_LINK="socks5://localhost:1080#it11-localhost"
WORK="$(mktemp -d /tmp/it11.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-11: $*"; exit 1; }

# ---- 1. 全新 xui-a、xui-b，各签发一个 admin 范围的 API token ----
"${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes xui-a xui-b >/dev/null 2>&1 || fail "启动 xui-a / xui-b 失败"
# xui_token <服务名> <面板地址> → 打印 token
xui_token() {
  local svc="$1" url="$2" jar="$WORK/$1.jar" user="" pw="" logs
  for _ in $(seq 1 30); do
    logs="$("${COMPOSE[@]}" logs "$svc" 2>/dev/null)"
    user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
    pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
    [ -n "$user" ] && curl -s -o /dev/null "$url/csrf-token" && break
    sleep 1
  done
  [ -n "$user" ] || return 1
  local csrf
  csrf="$(curl -s -b "$jar" -c "$jar" "$url/csrf-token" | jq -r .obj)"
  curl -s -b "$jar" -c "$jar" -X POST "$url/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" \
    --data "$(jq -nc --arg u "$user" --arg p "$pw" '{username:$u,password:$p}')" | jq -e .success >/dev/null || return 1
  csrf="$(curl -s -b "$jar" -c "$jar" "$url/csrf-token" | jq -r .obj)"
  curl -s -b "$jar" -c "$jar" -X POST "$url/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
    -H "X-CSRF-Token: $csrf" --data '{"name":"it11","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
TOKEN_A="$(xui_token xui-a "$XUI_A_URL")"; [ -n "$TOKEN_A" ] || fail "xui-a 签发 token 失败"
TOKEN_B="$(xui_token xui-b "$XUI_B_URL")"; [ -n "$TOKEN_B" ] || fail "xui-b 签发 token 失败"
# panel_api <面板地址> <token> <method> <path>：直接查面板（只读核对用）
panel_api() { curl -s -X "$3" "$1/panel/api$4" -H "Authorization: Bearer $2" -H 'Accept: application/json'; }
# template <a|b>：该面板当前的 Xray 模板
template() {
  local url="$XUI_A_URL" token="$TOKEN_A"
  [ "$1" = b ] && url="$XUI_B_URL" && token="$TOKEN_B"
  panel_api "$url" "$token" POST /xray/ | jq -r '.obj' | jq '.xraySetting | if type == "string" then fromjson else . end'
}
# hub_tags <a|b>：该面板模板里平台出站的 tag，逗号连接（没有为空串）
hub_tags() { template "$1" | jq -r '[.outbounds[] | select(.tag | startswith("hub-r")) | .tag] | join(",")'; }
# wait_tags <a 的期望> <b 的期望>：轮询到两台面板的平台出站都符合
wait_tags() {
  for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
    if [ "$(hub_tags a)" = "$1" ] && [ "$(hub_tags b)" = "$2" ]; then
      return 0
    fi
    sleep 1
  done
  fail "${APPLY_WAIT_SECONDS}s 内平台出站没有变成 a=[$1] b=[$2]：实际 a=[$(hub_tags a)] b=[$(hub_tags b)]，下发 $(hub GET "/api/admin/servers/$SERVER_A/chain-apply")"
}

# ---- 2. hub-server（下发检查 2 秒一次、同一服务器两次下发至少隔 5 秒、写操作上限放宽）与超管 ----
sed -e 's/^  applyInterval: .*/  applyInterval: 2s/' -e 's/^  minApplyInterval: .*/  minApplyInterval: 5s/' \
  -e "s/^  userWriteLimit: .*/  userWriteLimit: $WRITE_LIMIT/" "$REPO_ROOT/v2-hub-server/etc/hub-server.yaml" > "$WORK/hub-server.yaml"
grep -q 'applyInterval: 2s' "$WORK/hub-server.yaml" || fail "临时配置没有改到下发间隔"
grep -q "userWriteLimit: $WRITE_LIMIT" "$WORK/hub-server.yaml" || fail "临时配置没有改到写操作上限"
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml"
HUB_JAR="$WORK/hub.jar"
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
CSRF="$(curl -s -b "$HUB_JAR" -c "$HUB_JAR" -X POST "$BASE/api/admin/auth/login" -H 'Content-Type: application/json' --data "$(login_body_of "$ROOT_USER" "$CRED")" \
  | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body"
}
hub() { call "$HUB_JAR" "$CSRF" "$@"; }
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }

# ---- 3. 两台服务器各一个入站与直连节点；一个节点组、一个三种开关都开的套餐、两个用户 ----
# add_server <名称> <面板地址> <token> <端口> → 打印服务器 id
add_server() {
  local id
  id="$(hub POST /api/admin/servers "$(jq -nc --arg n "$1" --arg u "$2" --arg t "$3" '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
  expect_code 200 "接入服务器 $1"
  hub POST "/api/admin/servers/$id/inbounds" "$(jq -nc --arg r "$1-ws" --argjson p "$4" '{remark:$r,port:$p,template:"vmess-ws",templateParams:{path:"/it11"}}')" >/dev/null
  expect_code 200 "新建 $1 的入站"
  printf '%s' "$id"
}
SERVER_A="$(add_server it11-a "$XUI_A_URL" "$TOKEN_A" "$PORT_A")"
SERVER_B="$(add_server it11-b "$XUI_B_URL" "$TOKEN_B" "$PORT_B")"
nodes="$(hub GET "/api/admin/nodes?pageSize=100")"
node_of() { printf '%s' "$nodes" | jq -r --argjson s "$1" '[.data.items[] | select(.serverId == $s and .endpointId == null)][0].id'; }
NODE_A="$(node_of "$SERVER_A")"
NODE_B="$(node_of "$SERVER_B")"
[ "$NODE_A" != "null" ] && [ "$NODE_B" != "null" ] || fail "没找到两台服务器的直连节点：$nodes"
GROUP_ID="$(hub POST /api/admin/node-groups "$(jq -nc --argjson a "$NODE_A" --argjson b "$NODE_B" '{name:"it11-group",nodeIds:[$a,$b]}')" | jq -r .data.id)"
expect_code 200 "新建节点组"
PLAN_ID="$(hub POST /api/admin/plans "$(jq -nc --argjson g "$GROUP_ID" --argjson l "$GIB" \
  '{name:"it11-chain",trafficLimit:$l,durationDays:30,nodeGroupIds:[$g],allowClientChain:true,allowCustomNode:true,allowServerChain:true}')" | jq -r .data.id)"
expect_code 200 "新建套餐"
new_user() {
  local id
  id="$(hub POST /api/admin/users "$(login_body_of "$1" "$2" | jq -c --argjson plan "$PLAN_ID" '. + {planId:$plan}')" | jq -r '.data.id // empty')"
  expect_code 200 "新建用户 $1"
  hub POST "/api/admin/users/$id/reconcile" >/dev/null; expect_code 200 "对账 $1"
  printf '%s' "$id"
}
USER_ID="$(new_user "$END_USER" "$USER_CRED")"
OTHER_ID="$(new_user "$OTHER_USER" "$OTHER_CRED")"
provisions="$(hub GET "/api/admin/users/$USER_ID/provisions")"
EMAIL_A="$(printf '%s' "$provisions" | jq -r --argjson s "$SERVER_A" '.data[] | select(.serverId == $s) | .email')"
EMAIL_B="$(printf '%s' "$provisions" | jq -r --argjson s "$SERVER_B" '.data[] | select(.serverId == $s) | .email')"
[ -n "$EMAIL_A" ] && [ -n "$EMAIL_B" ] || fail "用户在两台面板上都应有 client：$provisions"

portal_login() { call "$1" "" POST /api/user/auth/login "$(login_body_of "$2" "$3")" | jq -r '.data.csrfToken // empty'; }
USER_JAR="$WORK/user.jar"
USER_CSRF="$(portal_login "$USER_JAR" "$END_USER" "$USER_CRED")"; [ -n "$USER_CSRF" ] || fail "终端用户登录失败"
portal() { call "$USER_JAR" "$USER_CSRF" "$@"; }
OTHER_JAR="$WORK/other.jar"
OTHER_CSRF="$(portal_login "$OTHER_JAR" "$OTHER_USER" "$OTHER_CRED")"; [ -n "$OTHER_CSRF" ] || fail "另一用户登录失败"
other() { call "$OTHER_JAR" "$OTHER_CSRF" "$@"; }

# ---- 4. 平台出口：没挂节点组时用户看不到；挂上后可见，用户侧不返回地址与链接；管理员取链接要单独请求 ----
exit_body() { jq -nc --arg n "$PLATFORM_NAME" --arg l "$PLATFORM_LINK" --argjson groups "$1" '{name:$n,link:$l,remark:"",isEnabled:true,allowServer:true,allowClient:true,nodeGroupIds:$groups}'; }
created="$(hub POST /api/admin/chain-exits "$(exit_body '[]')")"; expect_code 200 "新建平台出口"
PLATFORM_ID="$(printf '%s' "$created" | jq -r .data.id)"
printf '%s' "$created" | jq -e '.data | (has("link") | not) and .owner == "platform" and .user == null' >/dev/null || fail "平台出口的响应不应带链接：$created"
hub POST /api/admin/chain-exits "$(exit_body '[]')" >/dev/null; expect_code 409 "平台出口同名"
[ "$(portal GET /api/user/chain | jq '.data.platformExits | length')" = "0" ] || fail "没挂节点组的平台出口用户不应看到：$(cat "$WORK/body")"
# set_groups <节点组 id 的 JSON 数组>：整体更新平台出口，只换节点组
set_groups() { hub PUT "/api/admin/chain-exits/$PLATFORM_ID" "$(exit_body "$1" | jq -c 'del(.link)')" >/dev/null; expect_code 200 "更新平台出口的节点组"; }
set_groups "[$GROUP_ID]"
seen="$(portal GET /api/user/chain | jq -c '.data.platformExits')"
[ "$(printf '%s' "$seen" | jq -r '.[0].name')" = "$PLATFORM_NAME" ] || fail "挂上节点组后用户应看到平台出口：$seen"
printf '%s' "$seen" | jq -e '.[0] | (has("address") or has("link")) | not' >/dev/null || fail "用户侧不应返回平台出口的地址或链接：$seen"
[ "$(hub GET "/api/admin/chain-exits/$PLATFORM_ID/link" | jq -r .data.link)" = "$PLATFORM_LINK" ] || fail "管理员应能取到平台出口的链接"
portal GET "/api/user/chain/exits/$PLATFORM_ID/link" >/dev/null; expect_code 404 "用户取平台出口的链接"

# ---- 5. 一条规则两个起飞节点（分在两台面板）→ 平台出口：两台面板各一组 hub-r{id} 条目，路由带本人 email ----
# route_body <类型> <起飞节点 JSON 数组> <落地种类> <落地 id> [名称]
route_body() { jq -nc --arg k "$1" --argjson e "$2" --arg x "$3" --argjson id "$4" --arg n "${5:-}" '{kind:$k,entryNodeIds:$e,exitKind:$x,exitId:$id,name:$n,isEnabled:true}'; }
ROUTE_1="$(portal POST /api/user/chain/routes "$(route_body server "[$NODE_A,$NODE_B]" platform "$PLATFORM_ID")" | jq -r '.data.id // empty')"; expect_code 200 "新建服务端规则"
TAG_1="hub-r$ROUTE_1"
wait_tags "$TAG_1" "$TAG_1"
check_rule() {  # check_rule <a|b> <tag> <email>：该面板上这条路由只匹配一个入站、匹配本人 email、紧跟 api 规则
  local tpl rule
  tpl="$(template "$1")"
  rule="$(printf '%s' "$tpl" | jq -c --arg t "$2" '.routing.rules[] | select(.ruleTag == $t)')"
  [ "$(printf '%s' "$rule" | jq -r '.user | join(",")')" = "$3" ] || fail "面板 $1 的路由应只匹配本人 email：$rule"
  [ "$(printf '%s' "$rule" | jq -r '.inboundTag | length')" = "1" ] || fail "面板 $1 的路由应只匹配本面板的起飞入站：$rule"
  [ "$(printf '%s' "$tpl" | jq -r --arg t "$2" '.outbounds[] | select(.tag == $t) | .settings.servers[0].address')" = "xui-b" ] || fail "面板 $1 的出站应连到平台出口：$tpl"
}
check_rule a "$TAG_1" "$EMAIL_A"
check_rule b "$TAG_1" "$EMAIL_B"
states() { portal GET /api/user/chain | jq -r --argjson id "$1" '.data.serverRoutes[] | select(.id == $id) | .status + ":" + ([.entries[] | .state] | join(","))'; }
# wait_states <规则 id> <期望>：面板上先有条目、平台确认内核运行后才记为生效，所以状态要再等一下
wait_states() {
  for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
    [ "$(states "$1")" = "$2" ] && return 0
    sleep 1
  done
  fail "${APPLY_WAIT_SECONDS}s 内规则 $1 的状态没有变成 $2：实际 $(states "$1")"
}
wait_states "$ROUTE_1" "ok:applied,applied"
[ "$(hub GET "/api/admin/chain-routes?kind=server&serverId=$SERVER_B" | jq -r '(.data.total | tostring) + ":" + .data.items[0].user.username')" = "1:$END_USER" ] || fail "全站规则列表不对：$(cat "$WORK/body")"

# ---- 6. 我的出口用于服务端链式：回环地址、localhost 400；公网地址可以；客户端链式不限制 ----
add_exit() { portal POST /api/user/chain/exits "$(jq -nc --arg l "$1" '{link:$l}')" | jq -r '.data.id // empty'; }
LOOP_EXIT="$(add_exit "$OWN_LOOP_LINK")"; expect_code 200 "添加回环地址的出口（保存本身不限制）"
LOCALHOST_EXIT="$(add_exit "$OWN_LOCALHOST_LINK")"; expect_code 200 "添加 localhost 的出口"
OWN_EXIT="$(add_exit "$OWN_PUBLIC_LINK")"; expect_code 200 "添加公网地址的出口"
portal POST /api/user/chain/routes "$(route_body server "[$NODE_A]" own "$LOOP_EXIT")" >/dev/null; expect_code 400 "回环地址的出口用于服务端链式"
portal POST /api/user/chain/routes "$(route_body server "[$NODE_A]" own "$LOCALHOST_EXIT")" >/dev/null; expect_code 400 "localhost 的出口用于服务端链式"
LOOP_CLIENT="$(portal POST /api/user/chain/routes "$(route_body client "[$NODE_A]" own "$LOOP_EXIT")" | jq -r '.data.id // empty')"; expect_code 200 "回环地址的出口用于客户端链式"
[ -n "$LOOP_CLIENT" ] || fail "客户端规则没有建出来"

# ---- 7. 优先级：第 2 条（A → 我的出口）被第 1 条覆盖；调到最前后 A 归它，第 1 条只剩 B ----
second="$(portal POST /api/user/chain/routes "$(route_body server "[$NODE_A]" own "$OWN_EXIT" it11-own)")"; expect_code 200 "新建落地为我的出口的服务端规则"
ROUTE_2="$(printf '%s' "$second" | jq -r .data.id)"
TAG_2="hub-r$ROUTE_2"
[ "$(printf '%s' "$second" | jq -r '.data.entries[0].state + ":" + (.data.entries[0].coveredBy | tostring)')" = "shadowed:1" ] || fail "第 2 条的起飞节点应被第 1 条覆盖：$second"
order_body() { jq -nc --argjson ids "$1" '{kind:"server",ids:$ids}'; }
portal PUT /api/user/chain/routes/order "$(order_body "[$ROUTE_2]")" >/dev/null; expect_code 400 "顺序没带全"
portal PUT /api/user/chain/routes/order "$(order_body "[$ROUTE_2,$ROUTE_1]")" >/dev/null; expect_code 200 "把第 2 条调到最前"
wait_tags "$TAG_2" "$TAG_1"
[ "$(template a | jq -r --arg t "$TAG_2" '.outbounds[] | select(.tag == $t) | .settings.servers[0].address')" = "9.9.9.9" ] || fail "面板 a 的出站应连到我的出口：$(template a)"
check_rule b "$TAG_1" "$EMAIL_B"
wait_states "$ROUTE_1" "ok:shadowed,applied"
wait_states "$ROUTE_2" "ok:applied"
# 被服务端规则引用的出口换成回环地址：400，链接不变
portal PUT "/api/user/chain/exits/$OWN_EXIT" "$(jq -nc --arg l "$OWN_LOOP_LINK" '{name:"it11-own",link:$l}')" >/dev/null; expect_code 400 "被服务端规则引用的出口换成回环地址"
[ "$(portal GET "/api/user/chain/exits/$OWN_EXIT/link" | jq -r .data.link)" = "$OWN_PUBLIC_LINK" ] || fail "被拒绝的换链接不应生效"
# 防环：B 是起飞节点，不能再作落地；A 同理
portal POST /api/user/chain/routes "$(route_body server "[$NODE_B]" node "$NODE_A")" >/dev/null; expect_code 400 "落地是别的规则的起飞节点（成环）"

# ---- 8. 管理员管理用户出口：看得到归属与地址、看不了链接；停用后规则阻断，靠后的规则顶上 ----
listed="$(hub GET "/api/admin/chain-exits?owner=user&userId=$USER_ID&keyword=9.9.9")"; expect_code 200 "管理端按归属与关键字列出口"
[ "$(printf '%s' "$listed" | jq -r '(.data.total | tostring) + ":" + .data.items[0].user.username + ":" + .data.items[0].address')" = "1:$END_USER:9.9.9.9" ] || fail "管理端出口列表不对：$listed"
hub GET "/api/admin/chain-exits/$OWN_EXIT/link" >/dev/null; expect_code 404 "管理员取用户出口的链接"
hub PUT "/api/admin/chain-exits/$OWN_EXIT/enabled" '{"isEnabled":false}' >/dev/null; expect_code 200 "管理员停用用户出口"
wait_tags "$TAG_1" "$TAG_1"
[ "$(portal GET /api/user/chain | jq -r --argjson id "$ROUTE_2" '.data.serverRoutes[] | select(.id == $id) | .status + ":" + .blockedReason')" = "blocked:exit" ] || fail "出口停用后规则应阻断：$(cat "$WORK/body")"
[ "$(portal GET /api/user/chain | jq -r --argjson id "$OWN_EXIT" '.data.exits[] | select(.id == $id) | .isEnabled')" = "false" ] || fail "用户应看到出口已被停用"
hub PUT "/api/admin/chain-exits/$OWN_EXIT/enabled" '{"isEnabled":true}' >/dev/null; expect_code 200 "管理员启用用户出口"
wait_tags "$TAG_2" "$TAG_1"

# ---- 9. 他人的规则与出口一律 404（R8）；引用别人的出口 400；同套餐的用户看得到平台出口但看不到别人的规则 ----
other PUT "/api/user/chain/routes/$ROUTE_1" "$(route_body server "[$NODE_A]" platform "$PLATFORM_ID" | jq -c 'del(.kind)')" >/dev/null; expect_code 404 "改别人的规则"
other DELETE "/api/user/chain/routes/$ROUTE_1" >/dev/null; expect_code 404 "删别人的规则"
other PUT /api/user/chain/routes/order "$(order_body "[$ROUTE_1,$ROUTE_2]")" >/dev/null; expect_code 400 "给别人的规则调顺序"
other GET "/api/user/chain/exits/$OWN_EXIT/link" >/dev/null; expect_code 404 "看别人的出口链接"
other DELETE "/api/user/chain/exits/$OWN_EXIT" >/dev/null; expect_code 404 "删别人的出口"
other POST /api/user/chain/routes "$(route_body client "[$NODE_A]" own "$OWN_EXIT")" >/dev/null; expect_code 400 "用别人的出口作落地"
[ "$(other GET /api/user/chain | jq -c '[(.data.serverRoutes | length), (.data.clientRoutes | length), (.data.exits | length), (.data.platformExits | length)]')" = "[0,0,0,1]" ] \
  || fail "另一用户应只看到平台出口：$(cat "$WORK/body")"

# ---- 10. 客户端规则按（起飞，落地）组合进订阅：两个起飞节点 → 我的出口 = 两条链路，加上前面那条 = 三条 ----
portal POST /api/user/chain/routes "$(route_body client "[$NODE_A,$NODE_B]" own "$OWN_EXIT" it11-client)" >/dev/null; expect_code 200 "新建两个起飞节点的客户端规则"
SUB_URL="$(portal GET /api/user/subscription | jq -r .data.url)"; expect_code 200 "取订阅地址"
[ "$(curl -s "$SUB_URL?format=mihomo" | grep -c 'dialer-proxy')" = "3" ] || fail "Mihomo 订阅应有 3 条链路"
portal DELETE "/api/user/chain/exits/$LOOP_EXIT" >/dev/null; expect_code 200 "删除回环地址的出口（连带它的规则）"
[ "$(curl -s "$SUB_URL?format=mihomo" | grep -c 'dialer-proxy')" = "2" ] || fail "删掉出口后 Mihomo 订阅应剩 2 条链路"
[ "$(curl -s "$SUB_URL?format=mihomo" | grep -c 'it11-client · ')" -ge "2" ] || fail "起了名的规则每个起飞节点一条“规则名 · 起飞名”"

# ---- 11. 平台出口摘出节点组：第 1 条阻断，B 上的条目撤下；A 上仍是第 2 条 ----
set_groups '[]'
wait_tags "$TAG_2" ""
[ "$(portal GET /api/user/chain | jq -r --argjson id "$ROUTE_1" '.data.serverRoutes[] | select(.id == $id) | .status + ":" + .blockedReason')" = "blocked:exit" ] || fail "平台出口取消授权后规则应阻断：$(cat "$WORK/body")"
[ "$(panel_api "$XUI_A_URL" "$TOKEN_A" GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "面板 a 的 Xray 应在运行"
[ "$(panel_api "$XUI_B_URL" "$TOKEN_B" GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "面板 b 的 Xray 应在运行"

# ---- 11b. 改为指定用户（Ruling-64）：只给另一用户时本人仍阻断、另一用户可见；加上本人后第 1 条恢复，B 上的条目回来 ----
# assign_users <用户 id 的 JSON 数组>：整体更新平台出口，改为指定用户（节点组照传，服务端应清空）
assign_users() { hub PUT "/api/admin/chain-exits/$PLATFORM_ID" "$(exit_body "[$GROUP_ID]" | jq -c --argjson u "$1" 'del(.link) + {assignMode:"user",userIds:$u}')"; }
assign_users "[$OTHER_ID]" >/dev/null; expect_code 200 "平台出口改为指定另一用户"
[ "$(other GET /api/user/chain | jq '.data.platformExits | length')" = "1" ] || fail "被指定的用户应看到平台出口：$(cat "$WORK/body")"
[ "$(portal GET /api/user/chain | jq '.data.platformExits | length')" = "0" ] || fail "未被指定的用户不应看到平台出口：$(cat "$WORK/body")"
assigned="$(assign_users "[$USER_ID,$OTHER_ID]")"; expect_code 200 "平台出口指定两个用户"
printf '%s' "$assigned" | jq -e '.data | .assignMode == "user" and (.nodeGroups | length) == 0 and (.users | length) == 2' >/dev/null \
  || fail "指定用户后应只保留用户名单：$assigned"
wait_tags "$TAG_2" "$TAG_1"
[ "$(portal GET /api/user/chain | jq -r --argjson id "$ROUTE_1" '.data.serverRoutes[] | select(.id == $id) | .status')" = "ok" ] || fail "被指定后规则应恢复：$(cat "$WORK/body")"
assign_users '[999999999]' >/dev/null; expect_code 400 "指定不存在的用户"
hub PUT "/api/admin/chain-exits/$PLATFORM_ID" "$(exit_body '[]' | jq -c 'del(.link) + {assignMode:"nobody"}')" >/dev/null; expect_code 400 "分配方式取值非法"

# ---- 12. 写操作频控：连续调顺序，最终 429 rate_limited；客户端规则不受影响 ----
limited=""
for _ in $(seq 1 $((WRITE_LIMIT + 2))); do
  portal PUT /api/user/chain/routes/order "$(order_body "[$ROUTE_2,$ROUTE_1]")" >/dev/null
  if [ "$(cat "$WORK/code")" = "429" ]; then
    limited=1
    break
  fi
  expect_code 200 "频控触发前的调顺序"
done
[ -n "$limited" ] || fail "连续 $((WRITE_LIMIT + 2)) 次服务端规则写操作应触发 429"
[ "$(jq -r .code "$WORK/body")" = "rate_limited" ] || fail "错误码应为 rate_limited：$(cat "$WORK/body")"
portal DELETE "/api/user/chain/routes/$ROUTE_2" >/dev/null; expect_code 429 "频控期间删服务端规则"
portal POST /api/user/chain/routes "$(route_body client "[$NODE_B]" node "$NODE_A")" >/dev/null; expect_code 200 "频控期间客户端规则不受影响"

# ---- 13. 删除用户：规则随之删除，面板上的条目被清掉 ----
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
hub DELETE "/api/admin/users/$OTHER_ID" >/dev/null; expect_code 200 "删除另一用户"
wait_tags "" ""
hub DELETE "/api/admin/chain-exits/$PLATFORM_ID" >/dev/null; expect_code 200 "删除平台出口"
echo "IT-11 PASS"
