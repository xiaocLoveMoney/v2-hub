#!/usr/bin/env bash
# IT-9 服务端链式（docs/08，M7 DoD，docs/03 §5.19）：全新 xui-a（入口）与 xui-b（落地）→ 各一个 VMess+WS 入站，落地加对外地址 xui-b
# → 开了服务端链式的套餐与不开的套餐 → 用户配置“A → B”服务端规则 → 入口面板 Xray 模板出现 hub-r{id} 出站与按入站 + 用户匹配的路由且 Xray 运行
# → 在入口容器里用该用户的 Xray JSON 订阅起一个客户端发请求，落地面板上该用户的流量增加 → 换到不开的套餐后条目被清掉、换回后恢复
# → 他人 404 → 删除规则后条目被清掉。黑盒，只用 curl / jq / docker。
# 注意：会重建 xui-a、xui-b 两个容器（不挂数据卷），不影响 PostgreSQL 与 MinIO。hub-server 用缩短了下发间隔的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_A_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
XUI_B_URL="http://127.0.0.1:${XUI_B_PORT:-12054}"
ROOT_USER=it9-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it9-user
OTHER_USER=it9-other
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
OTHER_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
ENTRY_PORT=24901
EXIT_PORT=24902
EXIT_HOST=xui-b              # compose 网络里落地容器的名字：入口容器经它连到落地
CLIENT_SOCKS_PORT=10808      # 与 Xray JSON 订阅里客户端 socks 入站的端口一致
APPLY_WAIT_SECONDS=60
TRAFFIC_WAIT_SECONDS=40
PROBE_URL=http://example.com/
WORK="$(mktemp -d /tmp/it9.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-9: $*"; exit 1; }

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
    -H "X-CSRF-Token: $csrf" --data '{"name":"it9","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
TOKEN_A="$(xui_token xui-a "$XUI_A_URL")"; [ -n "$TOKEN_A" ] || fail "xui-a 签发 token 失败"
TOKEN_B="$(xui_token xui-b "$XUI_B_URL")"; [ -n "$TOKEN_B" ] || fail "xui-b 签发 token 失败"
# panel_api <面板地址> <token> <method> <path>：直接查面板（只读核对用）
panel_api() { curl -s -X "$3" "$1/panel/api$4" -H "Authorization: Bearer $2" -H 'Accept: application/json'; }
# entry_template：入口面板当前的 Xray 模板
entry_template() { panel_api "$XUI_A_URL" "$TOKEN_A" POST /xray/ | jq -r '.obj' | jq '.xraySetting | if type == "string" then fromjson else . end'; }
hub_entries() { entry_template | jq '[.outbounds[] | select(.tag | startswith("hub-"))] | length'; }

# ---- 2. hub-server（下发检查 2 秒一次、同一服务器两次下发至少隔 5 秒）与超管 ----
sed -e 's/^  applyInterval: .*/  applyInterval: 2s/' -e 's/^  minApplyInterval: .*/  minApplyInterval: 5s/' \
  "$REPO_ROOT/v2-hub-server/etc/hub-server.yaml" > "$WORK/hub-server.yaml"
grep -q 'applyInterval: 2s' "$WORK/hub-server.yaml" || fail "临时配置没有改到下发间隔"
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

# ---- 3. 两台服务器、各一个入站；落地入站加对外地址 xui-b（入口容器经 compose 网络连它） ----
# add_server <名称> <面板地址> <token> <端口> → 打印服务器 id
add_server() {
  local id
  id="$(hub POST /api/admin/servers "$(jq -nc --arg n "$1" --arg u "$2" --arg t "$3" '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
  expect_code 200 "接入服务器 $1"
  hub POST "/api/admin/servers/$id/inbounds" "$(jq -nc --arg r "$1-ws" --argjson p "$4" '{remark:$r,port:$p,template:"vmess-ws",templateParams:{path:"/it9"}}')" >/dev/null
  expect_code 200 "新建 $1 的入站"
  printf '%s' "$id"
}
SERVER_A="$(add_server it9-a "$XUI_A_URL" "$TOKEN_A" "$ENTRY_PORT")"
SERVER_B="$(add_server it9-b "$XUI_B_URL" "$TOKEN_B" "$EXIT_PORT")"
INBOUND_B="$(hub GET "/api/admin/servers/$SERVER_B/inbounds" | jq -r '.data[0].id')"
hub POST "/api/admin/servers/$SERVER_B/endpoints" "$(jq -nc --argjson id "$INBOUND_B" --arg h "$EXIT_HOST" '{inboundIds:[$id],addresses:[$h],port:0}')" >/dev/null
expect_code 200 "落地入站加对外地址"
nodes="$(hub GET "/api/admin/nodes?pageSize=100")"
ENTRY_NODE="$(printf '%s' "$nodes" | jq -r --argjson s "$SERVER_A" '[.data.items[] | select(.serverId == $s and .endpointId == null)][0].id')"
EXIT_NODE="$(printf '%s' "$nodes" | jq -r --argjson s "$SERVER_B" --arg h "$EXIT_HOST" '[.data.items[] | select(.serverId == $s and .address == $h)][0].id')"
ENTRY_NAME="$(printf '%s' "$nodes" | jq -r --argjson id "$ENTRY_NODE" '.data.items[] | select(.id == $id) | .displayName')"
[ "$ENTRY_NODE" != "null" ] && [ "$EXIT_NODE" != "null" ] || fail "没找到入口直连节点或落地 Host 节点：$nodes"
GROUP_ID="$(hub POST /api/admin/node-groups "$(jq -nc --argjson a "$ENTRY_NODE" --argjson b "$EXIT_NODE" '{name:"it9-group",nodeIds:[$a,$b]}')" | jq -r .data.id)"
expect_code 200 "新建节点组"
plan_body() { jq -nc --arg n "$1" --argjson c "$2" --argjson g "$GROUP_ID" --argjson l "$GIB" '{name:$n,trafficLimit:$l,durationDays:30,nodeGroupIds:[$g],allowServerChain:$c}'; }
RELAY_PLAN="$(hub POST /api/admin/plans "$(plan_body it9-relay true)" | jq -r .data.id)"; expect_code 200 "新建开服务端链式的套餐"
PLAIN_PLAN="$(hub POST /api/admin/plans "$(plan_body it9-plain false)" | jq -r .data.id)"; expect_code 200 "新建不开的套餐"
new_user() {
  local id
  id="$(hub POST /api/admin/users "$(login_body_of "$1" "$2" | jq -c --argjson plan "$RELAY_PLAN" '. + {planId:$plan}')" | jq -r '.data.id // empty')"
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

# ---- 4. 用户新建服务端规则；同一起飞节点再建一条会被靠前的覆盖；他人 404 ----
portal_login() { call "$1" "" POST /api/user/auth/login "$(login_body_of "$2" "$3")" | jq -r '.data.csrfToken // empty'; }
USER_JAR="$WORK/user.jar"
USER_CSRF="$(portal_login "$USER_JAR" "$END_USER" "$USER_CRED")"; [ -n "$USER_CSRF" ] || fail "终端用户登录失败"
portal() { call "$USER_JAR" "$USER_CSRF" "$@"; }
[ "$(portal GET /api/user/auth/me | jq -r .data.features.serverChain)" = "true" ] || fail "会话应带服务端链式开关"
[ "$(portal GET /api/user/chain | jq --argjson id "$ENTRY_NODE" '.data.nodes[] | select(.id == $id) | .canBeServerEntry')" = "true" ] || fail "入口节点应可作服务端入口"
rule_body="$(jq -nc --argjson e "$ENTRY_NODE" --argjson x "$EXIT_NODE" '{kind:"server",entryNodeIds:[$e],exitKind:"node",exitId:$x}')"
RULE_ID="$(portal POST /api/user/chain/routes "$rule_body" | jq -r '.data.id // empty')"; expect_code 200 "新建服务端规则"
later="$(portal POST /api/user/chain/routes "$rule_body")"; expect_code 200 "同一起飞节点再建一条"
[ "$(printf '%s' "$later" | jq -r '.data.entries[0].state + ":" + (.data.entries[0].coveredBy | tostring)')" = "shadowed:1" ] || fail "靠后的规则应被第 1 条覆盖：$later"
portal DELETE "/api/user/chain/routes/$(printf '%s' "$later" | jq -r .data.id)" >/dev/null; expect_code 200 "删除被覆盖的规则"
portal POST /api/user/chain/routes "$(jq -nc --argjson e "$EXIT_NODE" --argjson x "$ENTRY_NODE" '{kind:"server",entryNodeIds:[$e],exitKind:"node",exitId:$x}')" >/dev/null
expect_code 400 "反向再建一条会成环"
OTHER_JAR="$WORK/other.jar"
OTHER_CSRF="$(portal_login "$OTHER_JAR" "$OTHER_USER" "$OTHER_CRED")"
call "$OTHER_JAR" "$OTHER_CSRF" DELETE "/api/user/chain/routes/$RULE_ID" >/dev/null; expect_code 404 "删别人的规则"

# wait_apply <期望的“规则状态:起飞节点状态”> <期望平台条目数>：轮询到规则状态与入口模板都符合
wait_apply() {
  local want="$1" entries="$2" got=""
  for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
    got="$(portal GET /api/user/chain | jq -r '[.data.serverRoutes[] | .status + ":" + .entries[0].state] | join(",")')"
    if [ "$got" = "$want" ] && [ "$(hub_entries)" = "$entries" ]; then
      return 0
    fi
    sleep 1
  done
  fail "${APPLY_WAIT_SECONDS}s 内没有达到 $want / $entries 条平台条目：规则 $got，条目 $(hub_entries)，下发 $(hub GET "/api/admin/servers/$SERVER_A/chain-apply")"
}

# ---- 5. 下发：入口模板出现 hub-r{id} 出站（连 xui-b）与按入站 + 用户匹配的路由，Xray 运行 ----
wait_apply "ok:applied" 1
tpl="$(entry_template)"
TAG="hub-r$RULE_ID"
[ "$(printf '%s' "$tpl" | jq -r --arg t "$TAG" '.outbounds[] | select(.tag == $t) | .settings.vnext[0].address')" = "$EXIT_HOST" ] || fail "平台出站应连到 $EXIT_HOST：$tpl"
rule="$(printf '%s' "$tpl" | jq -c --arg t "$TAG" '.routing.rules[] | select(.ruleTag == $t)')"
[ "$(printf '%s' "$rule" | jq -r '.user[0]')" = "$EMAIL_A" ] || fail "路由应按本人在入口面板的 email 匹配：$rule"
[ "$(printf '%s' "$rule" | jq -r '.inboundTag | length')" = "1" ] || fail "路由应只匹配入口入站：$rule"
[ "$(printf '%s' "$tpl" | jq -r '[.routing.rules[] | (.ruleTag // .outboundTag)] | .[0:2] | join(",")')" = "api,$TAG" ] || fail "平台路由应紧跟 api 规则"
[ "$(panel_api "$XUI_A_URL" "$TOKEN_A" GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "入口 Xray 应在运行"
[ "$(hub GET "/api/admin/servers/$SERVER_A/chain-apply" | jq -r '.data.status + ":" + (.data.appliedRuleCount | tostring)')" = "applied:1" ] || fail "管理端下发状态不对：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/chain-routes?kind=server&serverId=$SERVER_A" | jq -r '(.data.total | tostring) + ":" + .data.items[0].entries[0].state')" = "1:applied" ] || fail "全站规则列表应有 1 条已生效的规则：$(cat "$WORK/body")"

# ---- 6. 真实流量：在入口容器里用本人的 Xray JSON 订阅起客户端发请求，落地面板上本人的流量增加 ----
b_traffic() { panel_api "$XUI_B_URL" "$TOKEN_B" GET /clients/list | jq -r --arg e "$EMAIL_B" '[.obj[] | select(.email == $e) | ((.traffic.up // 0) + (.traffic.down // 0))] | add // 0'; }
BEFORE="$(b_traffic)"
SUB_URL="$(portal GET /api/user/subscription | jq -r .data.url)"
curl -s "$SUB_URL?format=xray" | jq --arg n "$ENTRY_NAME" '[.[] | select(.remarks == $n)][0]' > "$WORK/client.json"
[ "$(jq -r '.outbounds[0].protocol' "$WORK/client.json")" = "vmess" ] || fail "没取到入口节点的 Xray 客户端配置"
XRAY_BIN="$("${COMPOSE[@]}" exec -T xui-a sh -c 'ls /app/bin/xray-linux-* | head -1' | tr -d '\r')"
# 客户端连的是入口直连节点（链接里的地址是 127.0.0.1，在入口容器里就是它自己）；请求能否到达外网不影响计数，字节经过落地即计入
"${COMPOSE[@]}" exec -T xui-a sh -c "cat > /tmp/it9-client.json" < "$WORK/client.json"
"${COMPOSE[@]}" exec -T xui-a sh -c "($XRAY_BIN run -c /tmp/it9-client.json >/tmp/it9-client.log 2>&1 &); sleep 2; \
  for i in 1 2 3; do curl -s -m 8 -o /dev/null -x socks5h://127.0.0.1:$CLIENT_SOCKS_PORT $PROBE_URL; done; \
  pkill -f it9-client.json || true" >/dev/null 2>&1 || true
grown=""
for _ in $(seq 1 "$TRAFFIC_WAIT_SECONDS"); do
  [ "$(b_traffic)" -gt "$BEFORE" ] && { grown=1; break; }
  sleep 1
done
[ -n "$grown" ] || fail "经入口的流量应计入落地面板上本人的用量（之前 $BEFORE，现在 $(b_traffic)）"

# ---- 7. 换到不开的套餐：阻断（plan），条目被清掉；换回后恢复 ----
hub POST "/api/admin/users/$USER_ID/plan" "$(jq -nc --argjson p "$PLAIN_PLAN" '{planId:$p}')" >/dev/null; expect_code 200 "换到不开的套餐"
portal POST /api/user/chain/routes "$rule_body" >/dev/null; expect_code 403 "套餐不允许时新建"
wait_apply "blocked:inactive" 0
hub POST "/api/admin/users/$USER_ID/plan" "$(jq -nc --argjson p "$RELAY_PLAN" '{planId:$p}')" >/dev/null; expect_code 200 "换回开的套餐"
wait_apply "ok:applied" 1

# ---- 8. 删除规则：条目被清掉，Xray 仍在运行；删除用户 ----
portal DELETE "/api/user/chain/routes/$RULE_ID" >/dev/null; expect_code 200 "删除服务端规则"
for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
  [ "$(hub_entries)" = "0" ] && break
  sleep 1
done
[ "$(hub_entries)" = "0" ] || fail "删除规则后入口模板里仍有平台条目"
[ "$(panel_api "$XUI_A_URL" "$TOKEN_A" GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "删除后入口 Xray 应在运行"
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
hub DELETE "/api/admin/users/$OTHER_ID" >/dev/null; expect_code 200 "删除另一用户"
echo "IT-9 PASS"
