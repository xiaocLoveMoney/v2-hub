#!/usr/bin/env bash
# IT-8 客户端链式（docs/08，M6 DoD，docs/03 §5.18）：全新 3x-ui → 两个入站（VMess+WS、VLESS+Reality）→ 开了链式的套餐与不开的套餐
# → 用户添加我的出口（SOCKS5）并配置“A 入口 → B 落地”“B 入口 → 自有落地”两条规则 → Mihomo / sing-box / Xray JSON 带链路且过真实客户端校验、
# Base64 不带 → 换到不开链式的套餐后规则阻断、写操作 403、订阅不再带链路 → 换回后自动恢复 → 他人的规则与节点 404。
# 黑盒，只用 curl / jq / docker。自有落地地址用文档保留网段（RFC 5737），平台不连接它（Ruling-38）。
# 注意：会重建 xui-a 容器（不挂数据卷，见 docker-compose.yml），不影响 PostgreSQL 与 MinIO。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
ROOT_USER=it8-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it8-user
OTHER_USER=it8-other
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
OTHER_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
WS_PORT=24801
REALITY_PORT=24802
NODE_COUNT=2
CHAIN_COUNT=2
CUSTOM_LINK="socks5://203.0.113.8:1080#it8-home"
CUSTOM_NAME=it8-home
HOME_RULE=it8-回家
ARROW="→"
SINGBOX_IMAGE="${IT_SINGBOX_IMAGE:-ghcr.io/sagernet/sing-box:latest}"
# 配置文件要挂进校验容器；macOS 上 mktemp 的默认目录不在 docker 的共享目录里，所以固定放 /tmp
WORK="$(mktemp -d /tmp/it8.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-8: $*"; exit 1; }

# ---- 1. 全新 3x-ui，按日志里的一次性账号登录并签发 admin 范围的 API token ----
"${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes xui-a >/dev/null 2>&1 || fail "启动 xui-a 失败"
xui_user="" xui_pw=""
for _ in $(seq 1 30); do
  logs="$("${COMPOSE[@]}" logs xui-a 2>/dev/null)"
  xui_user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
  xui_pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
  [ -n "$xui_user" ] && curl -s -o /dev/null "$XUI_URL/csrf-token" && break
  sleep 1
done
[ -n "$xui_user" ] || fail "没有在 xui-a 日志里找到初始账号"
XUI_JAR="$WORK/xui.jar"
xui_csrf() { curl -s -b "$XUI_JAR" -c "$XUI_JAR" "$XUI_URL/csrf-token" | jq -r .obj; }
login_body="$(jq -nc --arg u "$xui_user" --arg p "$xui_pw" '{username:$u,password:$p}')"
curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $(xui_csrf)" --data "$login_body" \
  | jq -e .success >/dev/null || fail "3x-ui 登录失败"
XUI_TOKEN="$(curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(xui_csrf)" --data '{"name":"it8","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty')"
[ -n "$XUI_TOKEN" ] || fail "签发 3x-ui API token 失败"

# ---- 2. hub-server 与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080
HUB_JAR="$WORK/hub.jar"
# login_body_of <用户名> <密码>
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
CSRF="$(curl -s -b "$HUB_JAR" -c "$HUB_JAR" -X POST "$BASE/api/admin/auth/login" -H 'Content-Type: application/json' --data "$(login_body_of "$ROOT_USER" "$CRED")" \
  | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
# call <cookie jar> <csrf> <method> <path> [json] → 打印响应体；状态码写入 $WORK/code
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body"
}
hub() { call "$HUB_JAR" "$CSRF" "$@"; }
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }

# ---- 3. 接入服务器与两个入站；两个套餐绑同一节点组，只有一个开链式与自有落地 ----
server="$(hub POST /api/admin/servers "$(jq -nc --arg u "$XUI_URL" --arg t "$XUI_TOKEN" '{name:"it8-a",providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')")"
expect_code 200 "接入服务器"
SERVER_ID="$(printf '%s' "$server" | jq -r .data.id)"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$(jq -nc --argjson p "$WS_PORT" '{remark:"it8-ws",port:$p,template:"vmess-ws",templateParams:{path:"/it8"}}')" >/dev/null
expect_code 200 "新建 VMess 入站"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" \
  "$(jq -nc --argjson p "$REALITY_PORT" '{remark:"it8-reality",port:$p,template:"vless-reality",templateParams:{target:"www.cloudflare.com:443"}}')" >/dev/null
expect_code 200 "新建 Reality 入站"
node_ids="$(hub GET "/api/admin/nodes?pageSize=100" | jq -c --argjson s "$SERVER_ID" '[.data.items[] | select(.serverId == $s) | .id]')"
[ "$(printf '%s' "$node_ids" | jq length)" = "$NODE_COUNT" ] || fail "应有 $NODE_COUNT 个直连节点：$node_ids"
GROUP_ID="$(hub POST /api/admin/node-groups "$(jq -nc --argjson ids "$node_ids" '{name:"it8-group",nodeIds:$ids}')" | jq -r .data.id)"; expect_code 200 "新建节点组"
# plan_body <名称> <是否开链式>
plan_body() {
  jq -nc --arg n "$1" --argjson c "$2" --argjson g "$GROUP_ID" --argjson l "$GIB" \
    '{name:$n,trafficLimit:$l,durationDays:30,nodeGroupIds:[$g],allowClientChain:$c,allowCustomNode:$c}'
}
CHAIN_PLAN="$(hub POST /api/admin/plans "$(plan_body it8-chain true)" | jq -r .data.id)"; expect_code 200 "新建开链式的套餐"
PLAIN_PLAN="$(hub POST /api/admin/plans "$(plan_body it8-plain false)" | jq -r .data.id)"; expect_code 200 "新建不开链式的套餐"
# new_user <用户名> <密码> → 打印用户 id
new_user() {
  local id
  id="$(hub POST /api/admin/users "$(login_body_of "$1" "$2" | jq -c --argjson plan "$CHAIN_PLAN" '. + {planId:$plan}')" | jq -r '.data.id // empty')"
  expect_code 200 "新建用户 $1"
  hub POST "/api/admin/users/$id/reconcile" >/dev/null; expect_code 200 "对账 $1"
  printf '%s' "$id"
}
USER_ID="$(new_user "$END_USER" "$USER_CRED")"
OTHER_ID="$(new_user "$OTHER_USER" "$OTHER_CRED")"

# ---- 4. 终端用户登录；会话带链式开关，概览给出可选节点 ----
# portal_login <cookie jar> <用户名> <密码> → 打印 CSRF token
portal_login() { call "$1" "" POST /api/user/auth/login "$(login_body_of "$2" "$3")" | jq -r '.data.csrfToken // empty'; }
USER_JAR="$WORK/user.jar"
USER_CSRF="$(portal_login "$USER_JAR" "$END_USER" "$USER_CRED")"
[ -n "$USER_CSRF" ] || fail "终端用户登录失败：$(cat "$WORK/body")"
portal() { call "$USER_JAR" "$USER_CSRF" "$@"; }
[ "$(portal GET /api/user/auth/me | jq -c '.data.features | {clientChain, customNode}')" = '{"clientChain":true,"customNode":true}' ] || fail "会话应带链式开关：$(cat "$WORK/body")"
overview="$(portal GET /api/user/chain)"; expect_code 200 "取链式概览"
[ "$(printf '%s' "$overview" | jq '.data.allowed and .data.exitAllowed')" = "true" ] || fail "概览应允许链式：$overview"
[ "$(printf '%s' "$overview" | jq '.data.nodes | length')" = "$NODE_COUNT" ] || fail "概览应给出 $NODE_COUNT 个可选节点：$overview"
NODE_A="$(printf '%s' "$overview" | jq -r '.data.nodes[0].id')"
NODE_B="$(printf '%s' "$overview" | jq -r '.data.nodes[1].id')"

# ---- 5. 我的出口与两条规则；坏链接 400、重复的组合标为 duplicate、起飞等于落地 400 ----
# http:// 与 https:// 自 M10 起是合法的代理出口（Ruling-51），这里用没有支持的协议
portal POST /api/user/chain/exits '{"link":"ftp://203.0.113.8:1080"}' >/dev/null; expect_code 400 "不支持的链接"
custom="$(portal POST /api/user/chain/exits "$(jq -nc --arg l "$CUSTOM_LINK" '{link:$l}')")"; expect_code 200 "添加我的出口"
CUSTOM_ID="$(printf '%s' "$custom" | jq -r .data.id)"
[ "$(printf '%s' "$custom" | jq -r '.data.name + " " + .data.protocol')" = "$CUSTOM_NAME socks" ] || fail "出口名称与协议不对：$custom"
printf '%s' "$custom" | jq -e '.data | has("link") | not' >/dev/null || fail "出口列表不应返回原链接"
[ "$(portal GET "/api/user/chain/exits/$CUSTOM_ID/link" | jq -r .data.link)" = "$CUSTOM_LINK" ] || fail "查看链接应返回原链接"
# rule_body <起飞节点> <落地节点> [是否启用]：客户端规则，落地是平台节点
rule_body() { jq -nc --argjson e "$1" --argjson x "$2" --argjson on "${3:-true}" '{kind:"client",entryNodeIds:[$e],exitKind:"node",exitId:$x,name:"",isEnabled:$on}'; }
RULE_AB="$(portal POST /api/user/chain/routes "$(rule_body "$NODE_A" "$NODE_B")" | jq -r .data.id)"; expect_code 200 "新建 A → B"
dup="$(portal POST /api/user/chain/routes "$(rule_body "$NODE_A" "$NODE_B")")"; expect_code 200 "重复的组合可以保存"
[ "$(printf '%s' "$dup" | jq -r '.data.entries[0].state + ":" + (.data.entries[0].coveredBy | tostring)')" = "duplicate:1" ] || fail "重复的组合应标为 duplicate 并指向第 1 条：$dup"
portal DELETE "/api/user/chain/routes/$(printf '%s' "$dup" | jq -r .data.id)" >/dev/null; expect_code 200 "删除重复的规则"
portal POST /api/user/chain/routes "$(rule_body "$NODE_A" "$NODE_A")" >/dev/null; expect_code 400 "起飞等于落地"
home="$(portal POST /api/user/chain/routes "$(jq -nc --argjson e "$NODE_B" --argjson c "$CUSTOM_ID" --arg n "$HOME_RULE" '{kind:"client",entryNodeIds:[$e],exitKind:"own",exitId:$c,name:$n,isEnabled:true}')")"
expect_code 200 "新建 B → 我的出口"
[ "$(printf '%s' "$home" | jq -r .data.status)" = "ok" ] || fail "新规则应通畅：$home"

# ---- 6. 订阅：三种格式带链路并过真实客户端校验，Base64 不带 ----
SUB_URL="$(portal GET /api/user/subscription | jq -r .data.url)"; expect_code 200 "取订阅地址"
# fetch <格式> <文件>：拉订阅正文到文件，打印状态码
fetch() { curl -s -o "$2" -w '%{http_code}' "$SUB_URL?format=$1"; }
b64d() { jq -Rrs 'gsub("\\s"; "") | @base64d'; }
# check_chains <期望链路数> [链路名片段...]：拉三种格式，数链路引用、核对名称并逐个用真实客户端校验
check_chains() {
  local want="$1" dir="$WORK/cfg-$1-$RANDOM"
  mkdir -p "$dir"
  [ "$(fetch mihomo "$dir/mihomo.yaml")" = "200" ] || fail "mihomo 订阅应为 200"
  [ "$(fetch singbox "$dir/singbox.json")" = "200" ] || fail "sing-box 订阅应为 200"
  [ "$(fetch xray "$dir/xray.json")" = "200" ] || fail "xray 订阅应为 200"
  local got
  got="$(grep -c 'dialer-proxy:' "$dir/mihomo.yaml" || true)"
  [ "$got" = "$want" ] || fail "mihomo 应有 $want 个 dialer-proxy，实际 $got"
  got="$(jq '[.outbounds[] | select(has("detour"))] | length' "$dir/singbox.json")"
  [ "$got" = "$want" ] || fail "sing-box 应有 $want 个带 detour 的出站，实际 $got"
  got="$(jq '[.[] | select([.outbounds[]? | .streamSettings.sockopt.dialerProxy? // empty] | length > 0)] | length' "$dir/xray.json")"
  [ "$got" = "$want" ] || fail "xray 应有 $want 份带 dialerProxy 的配置，实际 $got"
  # 其余参数是 mihomo 里必须出现的链路节点名（片段）
  local name
  for name in "${@:2}"; do
    grep -q -- "$name" "$dir/mihomo.yaml" || fail "mihomo 里应有链路节点“$name”：$(grep -n 'name:' "$dir/mihomo.yaml" | head -20)"
  done
  chmod -R a+rX "$dir"
  mihomo_test "$dir/mihomo.yaml" "$WORK/mihomo.log" "$WORK/mihomo-home" || fail "mihomo -t 未通过：$(tail -5 "$WORK/mihomo.log")"
  docker run --rm -v "$dir:/cfg:ro" "$SINGBOX_IMAGE" check -c /cfg/singbox.json >"$WORK/singbox.log" 2>&1 || fail "sing-box check 未通过：$(tail -5 "$WORK/singbox.log")"
  local xray_bin n
  xray_bin="$("${COMPOSE[@]}" exec -T xui-a sh -c 'ls /app/bin/xray-linux-* | head -1' | tr -d '\r')"
  [ -n "$xray_bin" ] || fail "面板容器里没有找到 xray"
  n="$(jq length "$dir/xray.json")"
  for i in $(seq 0 $((n - 1))); do
    jq ".[$i]" "$dir/xray.json" | "${COMPOSE[@]}" exec -T -w /app/bin xui-a "$xray_bin" run -test -c stdin: -format json >"$WORK/xray.log" 2>&1 \
      || fail "xray -test 未通过（第 $i 份）：$(tail -3 "$WORK/xray.log")"
  done
}
check_chains "$CHAIN_COUNT" "$ARROW" "$HOME_RULE"
[ "$(fetch base64 "$WORK/sub.b64")" = "200" ] || fail "base64 订阅应为 200"
if b64d < "$WORK/sub.b64" | grep -q "$ARROW\|socks5://"; then fail "base64 订阅不应带链路或出口"; fi

# ---- 7. 他人的规则与出口一律 404（R8）；引用别人的出口与引用不存在的出口同样 400 ----
OTHER_JAR="$WORK/other.jar"
OTHER_CSRF="$(portal_login "$OTHER_JAR" "$OTHER_USER" "$OTHER_CRED")"
[ -n "$OTHER_CSRF" ] || fail "另一用户登录失败"
other() { call "$OTHER_JAR" "$OTHER_CSRF" "$@"; }
[ "$(other GET /api/user/chain | jq '(.data.clientRoutes | length) + (.data.exits | length)')" = "0" ] || fail "另一用户不应看到别人的规则与出口"
other PUT "/api/user/chain/routes/$RULE_AB" "$(rule_body "$NODE_A" "$NODE_B" false)" >/dev/null; expect_code 404 "改别人的规则"
other DELETE "/api/user/chain/routes/$RULE_AB" >/dev/null; expect_code 404 "删别人的规则"
other GET "/api/user/chain/exits/$CUSTOM_ID/link" >/dev/null; expect_code 404 "看别人的出口链接"
other POST /api/user/chain/routes "$(jq -nc --argjson e "$NODE_A" --argjson c "$CUSTOM_ID" '{kind:"client",entryNodeIds:[$e],exitKind:"own",exitId:$c,isEnabled:true}')" >/dev/null
expect_code 400 "用别人的出口作落地"

# ---- 8. 停用一条：订阅少一条链路 ----
portal PUT "/api/user/chain/routes/$RULE_AB" "$(rule_body "$NODE_A" "$NODE_B" false)" >/dev/null; expect_code 200 "停用 A → B"
check_chains 1 "$HOME_RULE"
portal PUT "/api/user/chain/routes/$RULE_AB" "$(rule_body "$NODE_A" "$NODE_B")" >/dev/null; expect_code 200 "启用 A → B"

# ---- 9. 换到不开链式的套餐：规则阻断（原因 plan）、写操作 403、删除仍可、订阅不带链路 ----
hub POST "/api/admin/users/$USER_ID/plan" "$(jq -nc --argjson p "$PLAIN_PLAN" '{planId:$p}')" >/dev/null; expect_code 200 "换到不开链式的套餐"
[ "$(portal GET /api/user/auth/me | jq -c '.data.features | {clientChain, customNode}')" = '{"clientChain":false,"customNode":false}' ] || fail "换套餐后会话开关应关闭：$(cat "$WORK/body")"
blocked="$(portal GET /api/user/chain)"
[ "$(printf '%s' "$blocked" | jq -r '[.data.clientRoutes[] | .status + ":" + .blockedReason] | unique | join(",")')" = "blocked:plan" ] || fail "规则应因套餐阻断：$blocked"
portal POST /api/user/chain/routes "$(rule_body "$NODE_B" "$NODE_A")" >/dev/null; expect_code 403 "套餐不允许时新建规则"
[ "$(jq -r .code "$WORK/body")" = "feature_disabled" ] || fail "错误码应为 feature_disabled：$(cat "$WORK/body")"
portal POST /api/user/chain/exits "$(jq -nc --arg l "$CUSTOM_LINK" '{link:$l, name:"it8-two"}')" >/dev/null; expect_code 403 "套餐不允许时添加出口"
check_chains 0

# ---- 10. 换回开链式的套餐：规则自动恢复通畅 ----
hub POST "/api/admin/users/$USER_ID/plan" "$(jq -nc --argjson p "$CHAIN_PLAN" '{planId:$p}')" >/dev/null; expect_code 200 "换回开链式的套餐"
[ "$(portal GET /api/user/chain | jq -r '[.data.clientRoutes[].status] | unique | join(",")')" = "ok" ] || fail "换回后规则应自动通畅：$(cat "$WORK/body")"
check_chains "$CHAIN_COUNT" "$ARROW" "$HOME_RULE"

# ---- 11. 删除出口连带删除引用它的规则；删除用户后订阅 404 ----
portal DELETE "/api/user/chain/exits/$CUSTOM_ID" >/dev/null; expect_code 200 "删除出口"
[ "$(portal GET /api/user/chain | jq '.data.clientRoutes | length')" = "1" ] || fail "删除出口后应只剩 1 条规则：$(cat "$WORK/body")"
check_chains 1 "$ARROW"
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
hub DELETE "/api/admin/users/$OTHER_ID" >/dev/null; expect_code 200 "删除另一用户"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SUB_URL")" = "404" ] || fail "删除用户后订阅地址应为 404"
echo "IT-8 PASS"
