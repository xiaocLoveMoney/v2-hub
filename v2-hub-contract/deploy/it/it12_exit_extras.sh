#!/usr/bin/env bash
# IT-12 出口补全（docs/08，M10 DoD，docs/03 §5.21，Ruling-51 – Ruling-55）：全新 xui-a 一个 VMess+WS 入站
# → 接入后面板模板自动出现平台拦截规则 hub-block（黑洞出站 + 紧跟 api 的路由），Xray 运行
# → 用户添加 http://、https:// 出口（网页地址 400），列表里协议为 http
# → 用 HTTP 出口建服务端规则，下发后面板模板里是 http 协议的 hub-r{id} 出站；换成 HTTPS 出口后出站带 TLS
# → 客户端规则进订阅，Mihomo / sing-box / Xray JSON 各自通过配置检查
# → 延迟测试：公网出口三种方式都拿到结果（连得上连不上都算）；回环地址的出口 400；别人的出口 404；管理员测平台出口带错误原文字段；
#   连续测试触发 429
# → 取面板网页证书路径返回两个字段 → REALITY 目标扫描：公网目标拿到结论，网段与网址 400，回环地址只标记不探测
# → 删除用户后链式条目清掉、拦截规则仍在 → 关掉设置 routing.blockRule 后拦截规则撤掉。
# 黑盒，只用 curl / jq / docker。延迟测试由 xui-a 容器去连出口：出口用公网 IP 字面量，容器没有外网时结果是“不通”，用例照样通过。
# 注意：会重建 xui-a 容器（不挂数据卷），不影响 PostgreSQL 与 MinIO。hub-server 用缩短了下发间隔的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_A_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
MIHOMO_IMAGE="${IT_MIHOMO_IMAGE:-metacubex/mihomo:latest}"
SINGBOX_IMAGE="${IT_SINGBOX_IMAGE:-ghcr.io/sagernet/sing-box:latest}"
ROOT_USER=it12-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it12-user
OTHER_USER=it12-other
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
OTHER_CRED="it-$(openssl rand -hex 12)"
PROXY_AUTH="it-$(openssl rand -hex 6)"   # 出口的示例口令，不对应任何真实代理
GIB=1073741824
PORT_A=25201
APPLY_WAIT_SECONDS=60
TEST_LIMIT=6                 # 临时配置里每用户每分钟的测试次数：前面的步骤用掉 4 次，最后专门打满
HTTP_LINK="http://it12-proxy:${PROXY_AUTH}@9.9.9.9:8080#it12-http"   # 公网地址；下发只写配置，不会去连
HTTPS_LINK="https://1.1.1.1:443?sni=one.one.one.one#it12-https"
LOOP_LINK="http://127.0.0.1:8080#it12-loop"
PAGE_LINK="https://one.one.one.one/index.html"
PLATFORM_LINK="socks5://xui-a:1080#it12-platform"   # 平台出口不受地址限制（Ruling-48）
WORK="$(mktemp -d /tmp/it12.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-12: $*"; exit 1; }

# ---- 1. 全新 xui-a，签发一个 admin 范围的 API token ----
"${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes xui-a >/dev/null 2>&1 || fail "启动 xui-a 失败"
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
    -H "X-CSRF-Token: $csrf" --data '{"name":"it12","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
TOKEN_A="$(xui_token xui-a "$XUI_A_URL")"; [ -n "$TOKEN_A" ] || fail "xui-a 签发 token 失败"
panel_api() { curl -s -X "$1" "$XUI_A_URL/panel/api$2" -H "Authorization: Bearer $TOKEN_A" -H 'Accept: application/json'; }
template() { panel_api POST /xray/ | jq -r '.obj' | jq '.xraySetting | if type == "string" then fromjson else . end'; }
# hub_outbound <tag>：模板里这个平台出站（没有则为空）
hub_outbound() { template | jq -c --arg t "$1" '.outbounds[] | select(.tag == $t)'; }
# wait_outbound <tag> <jq 条件>：轮询到该出站存在且满足条件
wait_outbound() {
  for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
    if hub_outbound "$1" | jq -e "$2" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "${APPLY_WAIT_SECONDS}s 内出站 $1 没有满足 $2：实际 $(hub_outbound "$1" | jq -c 'del(.settings)')，下发 $(hub GET "/api/admin/servers/$SERVER_A/chain-apply")"
}

# ---- 2. hub-server（下发检查 2 秒一次、同一服务器两次下发至少隔 5 秒、测试次数上限改小）与超管 ----
sed -e 's/^  applyInterval: .*/  applyInterval: 2s/' -e 's/^  minApplyInterval: .*/  minApplyInterval: 5s/' \
  -e "s/^  userTestLimit: .*/  userTestLimit: $TEST_LIMIT/" "$REPO_ROOT/v2-hub-server/etc/hub-server.yaml" > "$WORK/hub-server.yaml"
grep -q 'applyInterval: 2s' "$WORK/hub-server.yaml" || fail "临时配置没有改到下发间隔"
grep -q "userTestLimit: $TEST_LIMIT" "$WORK/hub-server.yaml" || fail "临时配置没有改到测试次数上限"
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

# ---- 3. 一台服务器、一个入站与直连节点；节点组、三种开关都开的套餐、两个用户 ----
SERVER_A="$(hub POST /api/admin/servers "$(jq -nc --arg u "$XUI_A_URL" --arg t "$TOKEN_A" '{name:"it12-a",providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
hub POST "/api/admin/servers/$SERVER_A/inbounds" "$(jq -nc --argjson p "$PORT_A" '{remark:"it12-ws",port:$p,template:"vmess-ws",templateParams:{path:"/it12"}}')" >/dev/null
expect_code 200 "新建入站"
NODE_A="$(hub GET "/api/admin/nodes?pageSize=100" | jq -r --argjson s "$SERVER_A" '[.data.items[] | select(.serverId == $s and .endpointId == null)][0].id')"
[ "$NODE_A" != "null" ] || fail "没找到直连节点"
GROUP_ID="$(hub POST /api/admin/node-groups "$(jq -nc --argjson a "$NODE_A" '{name:"it12-group",nodeIds:[$a]}')" | jq -r .data.id)"
expect_code 200 "新建节点组"
PLAN_ID="$(hub POST /api/admin/plans "$(jq -nc --argjson g "$GROUP_ID" --argjson l "$GIB" \
  '{name:"it12-chain",trafficLimit:$l,durationDays:30,nodeGroupIds:[$g],allowClientChain:true,allowCustomNode:true,allowServerChain:true}')" | jq -r .data.id)"
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
portal_login() { call "$1" "" POST /api/user/auth/login "$(login_body_of "$2" "$3")" | jq -r '.data.csrfToken // empty'; }
USER_JAR="$WORK/user.jar"
USER_CSRF="$(portal_login "$USER_JAR" "$END_USER" "$USER_CRED")"; [ -n "$USER_CSRF" ] || fail "终端用户登录失败"
portal() { call "$USER_JAR" "$USER_CSRF" "$@"; }
OTHER_JAR="$WORK/other.jar"
OTHER_CSRF="$(portal_login "$OTHER_JAR" "$OTHER_USER" "$OTHER_CRED")"; [ -n "$OTHER_CSRF" ] || fail "另一用户登录失败"
other() { call "$OTHER_JAR" "$OTHER_CSRF" "$@"; }

# ---- 3b. 平台拦截规则（Ruling-55，默认开）：接入后模板出现 hub-block 黑洞出站与紧跟 api 的拦截路由，Xray 运行 ----
BLOCK_TAG=hub-block
[ "$(hub GET /api/admin/settings | jq -r .data.routing.blockRule)" = "true" ] || fail "routing.blockRule 默认应为开"
wait_outbound "$BLOCK_TAG" '.protocol == "blackhole"'
tpl="$(template)"
block_rule="$(printf '%s' "$tpl" | jq -c --arg t "$BLOCK_TAG" '.routing.rules[] | select(.ruleTag == $t)')"
printf '%s' "$block_rule" | jq -e --arg t "$BLOCK_TAG" '.outboundTag == $t and (.domain | index("geosite:category-ads-all") != null)
  and (.domain | map(select(startswith("regexp:"))) | length) == 7' >/dev/null || fail "拦截路由不对：$block_rule"
[ "$(printf '%s' "$tpl" | jq -r '[.routing.rules[] | (.ruleTag // .outboundTag)] | .[0:2] | join(",")')" = "api,$BLOCK_TAG" ] || fail "拦截路由应紧跟 api 规则"
for _ in $(seq 1 15); do
  [ "$(panel_api GET /server/status | jq -r .obj.xray.state)" = "running" ] && break
  sleep 1
done
[ "$(panel_api GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "加拦截规则后面板的 Xray 应在运行"

# ---- 4. HTTP / HTTPS 出口：网页地址 400；建出来协议都是 http，名称取自链接 ----
add_exit() { portal POST /api/user/chain/exits "$(jq -nc --arg l "$1" '{link:$l}')"; }
add_exit "$PAGE_LINK" >/dev/null; expect_code 400 "带路径的网页地址当出口"
HTTP_EXIT="$(add_exit "$HTTP_LINK" | jq -r '.data.id // empty')"; expect_code 200 "添加 HTTP 出口"
HTTPS_EXIT="$(add_exit "$HTTPS_LINK" | jq -r '.data.id // empty')"; expect_code 200 "添加 HTTPS 出口"
LOOP_EXIT="$(add_exit "$LOOP_LINK" | jq -r '.data.id // empty')"; expect_code 200 "添加回环地址的 HTTP 出口（客户端链式可用）"
exits="$(portal GET /api/user/chain | jq -c '[.data.exits[] | {name, protocol, port}]')"
[ "$(printf '%s' "$exits" | jq -c 'map(select(.name == "it12-http" or .name == "it12-https")) | map(.protocol) | unique')" = '["http"]' ] || fail "HTTP / HTTPS 出口的协议应为 http：$exits"
printf '%s' "$exits" | jq -e 'has(0) and (map(has("link")) | any | not)' >/dev/null || fail "出口列表不应带链接：$exits"
[ "$(portal GET "/api/user/chain/exits/$HTTP_EXIT/link" | jq -r .data.link)" = "$HTTP_LINK" ] || fail "取回的 HTTP 出口链接应与填的一致"

# ---- 5. 服务端规则用 HTTP 出口：面板模板里是 http 协议的出站，带用户名；换成 HTTPS 出口后带 TLS 与 SNI ----
route_body() { jq -nc --arg k "$1" --argjson e "[$NODE_A]" --arg x "$2" --argjson id "$3" --arg n "${4:-}" '{kind:$k,entryNodeIds:$e,exitKind:$x,exitId:$id,name:$n,isEnabled:true}'; }
ROUTE_1="$(portal POST /api/user/chain/routes "$(route_body server own "$HTTP_EXIT")" | jq -r '.data.id // empty')"; expect_code 200 "新建落地为 HTTP 出口的服务端规则"
TAG_1="hub-r$ROUTE_1"
wait_outbound "$TAG_1" '.protocol == "http" and .settings.servers[0].address == "9.9.9.9" and .settings.servers[0].port == 8080 and .settings.servers[0].users[0].user == "it12-proxy" and .streamSettings.security == "none"'
portal POST /api/user/chain/routes "$(route_body server own "$LOOP_EXIT")" >/dev/null; expect_code 400 "回环地址的 HTTP 出口用于服务端链式"
portal PUT "/api/user/chain/routes/$ROUTE_1" "$(route_body server own "$HTTPS_EXIT" | jq -c 'del(.kind)')" >/dev/null; expect_code 200 "规则换成 HTTPS 出口"
wait_outbound "$TAG_1" '.protocol == "http" and .settings.servers[0].address == "1.1.1.1" and .streamSettings.security == "tls" and .streamSettings.tlsSettings.serverName == "one.one.one.one"'
[ "$(panel_api GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "下发 HTTP 出站后面板的 Xray 应在运行"

# ---- 6. 客户端规则进订阅：三种带链路的格式各自通过配置检查 ----
portal POST /api/user/chain/routes "$(route_body client own "$HTTP_EXIT" it12-http-chain)" >/dev/null; expect_code 200 "新建落地为 HTTP 出口的客户端规则"
portal POST /api/user/chain/routes "$(route_body client own "$HTTPS_EXIT" it12-https-chain)" >/dev/null; expect_code 200 "新建落地为 HTTPS 出口的客户端规则"
SUB_URL="$(portal GET /api/user/subscription | jq -r .data.url)"; expect_code 200 "取订阅地址"
SUB_DIR="$WORK/sub"; mkdir -p "$SUB_DIR"
curl -s "$SUB_URL?format=mihomo" -o "$SUB_DIR/mihomo.yaml"
curl -s "$SUB_URL?format=singbox" -o "$SUB_DIR/singbox.json"
curl -s "$SUB_URL?format=xray" -o "$SUB_DIR/xray.json"
[ "$(grep -c 'dialer-proxy:' "$SUB_DIR/mihomo.yaml" || true)" = "2" ] || fail "Mihomo 订阅应有 2 条链路"
[ "$(grep -c 'type: http' "$SUB_DIR/mihomo.yaml" || true)" = "2" ] || fail "Mihomo 订阅里两个落地都应是 http 类型"
[ "$(jq '[.outbounds[] | select(.type == "http")] | length' "$SUB_DIR/singbox.json")" = "2" ] || fail "sing-box 订阅应有 2 个 http 出站"
[ "$(jq '[.outbounds[] | select(.type == "http" and .tls.enabled == true)] | length' "$SUB_DIR/singbox.json")" = "1" ] || fail "sing-box 订阅里 HTTPS 出口应带 TLS"
[ "$(jq '[.[] | select([.outbounds[]? | select(.protocol == "http")] | length > 0)] | length' "$SUB_DIR/xray.json")" = "2" ] || fail "Xray JSON 订阅应有 2 份带 http 出站的配置"
chmod -R a+rX "$SUB_DIR"
docker run --rm -v "$SUB_DIR:/cfg:ro" "$MIHOMO_IMAGE" -t -f /cfg/mihomo.yaml -d /tmp >"$WORK/mihomo.log" 2>&1 || fail "mihomo -t 未通过：$(tail -5 "$WORK/mihomo.log")"
docker run --rm -v "$SUB_DIR:/cfg:ro" "$SINGBOX_IMAGE" check -c /cfg/singbox.json >"$WORK/singbox.log" 2>&1 || fail "sing-box check 未通过：$(tail -5 "$WORK/singbox.log")"
XRAY_BIN="$("${COMPOSE[@]}" exec -T xui-a sh -c 'ls /app/bin/xray-linux-* | head -1' | tr -d '\r')"
[ -n "$XRAY_BIN" ] || fail "面板容器里没有找到 xray"
for i in $(seq 0 $(($(jq length "$SUB_DIR/xray.json") - 1))); do
  jq ".[$i]" "$SUB_DIR/xray.json" | "${COMPOSE[@]}" exec -T -w /app/bin xui-a "$XRAY_BIN" run -test -c stdin: -format json >"$WORK/xray.log" 2>&1 \
    || fail "xray -test 未通过（第 $i 份）：$(tail -3 "$WORK/xray.log")"
done

# ---- 7. 延迟测试：三种方式都拿到结果；校验、归属与地址限制 ----
test_body() { jq -nc --argjson n "$1" --arg m "$2" '{nodeId:$n,mode:$m}'; }
# run_test <出口 id> <方式>：发起一次测试，核对结果的形状，打印“方式=结果”
run_test() {
  local res
  res="$(portal POST "/api/user/chain/exits/$1/test" "$(test_body "$NODE_A" "$2")")"; expect_code 200 "测试出口 $1（$2）"
  printf '%s' "$res" | jq -e '.data | (.ok | type == "boolean") and (.delayMs | type == "number") and (.httpStatus | type == "number")
    and (.egressIp | type == "string") and (has("error") | not) and (.mode == "tcp" or .mode == "http" or .mode == "real")' >/dev/null \
    || fail "测试结果的形状不对（用户侧不应带 error）：$res"
  # Docker Desktop 的网络栈会在本地应答 TCP 握手，tcp 方式的延迟可能是 0 毫秒，所以只要求不通时为 0
  printf '%s' "$res" | jq -e '.data | .delayMs >= 0 and (.ok or .delayMs == 0)' >/dev/null || fail "不通时延迟应为 0：$res"
  echo "  测试 $2：$(printf '%s' "$res" | jq -c '.data | {ok, delayMs, httpStatus, egressIp}')"
}
run_test "$HTTPS_EXIT" tcp     # 第 1 次
run_test "$HTTPS_EXIT" http    # 第 2 次
run_test "$HTTP_EXIT" real     # 第 3 次
[ "$(panel_api GET /server/status | jq -r .obj.xray.state)" = "running" ] || fail "测试之后面板的 Xray 应在运行"
[ -n "$(hub_outbound "$TAG_1")" ] || fail "测试不应动面板模板里的平台条目"
# 不计入频控的拒绝：方式不对、别人的出口、不存在的起点
portal POST "/api/user/chain/exits/$HTTP_EXIT/test" "$(test_body "$NODE_A" icmp)" >/dev/null; expect_code 400 "未知的测试方式"
other POST "/api/user/chain/exits/$HTTP_EXIT/test" "$(test_body "$NODE_A" tcp)" >/dev/null; expect_code 404 "测试别人的出口"
portal POST "/api/user/chain/exits/$HTTP_EXIT/test" "$(test_body 999999 tcp)" >/dev/null; expect_code 400 "从不存在的节点发起测试"
# 回环地址的出口：面板会去连这个地址，所以不许测（第 4 次，计入频控）
portal POST "/api/user/chain/exits/$LOOP_EXIT/test" "$(test_body "$NODE_A" tcp)" >/dev/null; expect_code 400 "测试回环地址的出口"

# ---- 8. 管理员测试：平台出口不受地址限制，结果带错误原文字段；用户出口同样先过地址限制 ----
PLATFORM_ID="$(hub POST /api/admin/chain-exits "$(jq -nc --arg l "$PLATFORM_LINK" --argjson g "[$GROUP_ID]" \
  '{name:"it12-平台出口",link:$l,remark:"",isEnabled:true,allowServer:true,allowClient:false,nodeGroupIds:$g}')" | jq -r '.data.id // empty')"; expect_code 200 "新建平台出口"
admin_test() { hub POST "/api/admin/chain-exits/$1/test" "$(jq -nc --argjson s "$2" --arg m "$3" '{serverId:$s,mode:$m}')"; }
res="$(admin_test "$PLATFORM_ID" "$SERVER_A" tcp)"; expect_code 200 "管理员测试平台出口"
printf '%s' "$res" | jq -e '.data | (.ok | type == "boolean") and (.error | type == "string")' >/dev/null || fail "管理端测试结果应带 error 字段：$res"
echo "  管理员测试平台出口：$(printf '%s' "$res" | jq -c '.data | {ok, delayMs, error}')"
admin_test "$LOOP_EXIT" "$SERVER_A" tcp >/dev/null; expect_code 400 "管理员测试回环地址的用户出口"
admin_test "$PLATFORM_ID" 999999 tcp >/dev/null; expect_code 404 "从不存在的服务器发起测试"
portal POST "/api/user/chain/exits/$PLATFORM_ID/test" "$(test_body "$NODE_A" tcp)" >/dev/null; expect_code 404 "用户测试平台出口"

# ---- 9. 测试频控：连续测试，最终 429 rate_limited；管理员不受影响 ----
limited=""
for _ in $(seq 1 $((TEST_LIMIT + 2))); do
  portal POST "/api/user/chain/exits/$LOOP_EXIT/test" "$(test_body "$NODE_A" tcp)" >/dev/null
  if [ "$(cat "$WORK/code")" = "429" ]; then
    limited=1
    break
  fi
  expect_code 400 "频控触发前测试回环地址的出口"
done
[ -n "$limited" ] || fail "连续 $((TEST_LIMIT + 2)) 次测试应触发 429"
[ "$(jq -r .code "$WORK/body")" = "rate_limited" ] || fail "错误码应为 rate_limited：$(cat "$WORK/body")"
admin_test "$PLATFORM_ID" "$SERVER_A" tcp >/dev/null; expect_code 200 "用户被频控时管理员的测试不受影响"

# ---- 10. 取面板的网页证书路径：两个字段都在（测试面板没配证书，是空串） ----
hub GET "/api/admin/servers/$SERVER_A/web-cert" | jq -e '.data | (.certFile | type == "string") and (.keyFile | type == "string")' >/dev/null \
  || fail "取面板证书路径应返回 certFile 与 keyFile：$(cat "$WORK/body")"
expect_code 200 "取面板证书路径"
hub GET /api/admin/servers/999999/web-cert >/dev/null; expect_code 404 "取不存在的服务器的证书路径"
portal GET "/api/admin/servers/$SERVER_A/web-cert" >/dev/null; expect_code 401 "终端用户取面板证书路径"

# ---- 11. REALITY 目标扫描（Ruling-54）：由面板去探测；网段与网址 400，内网目标不探测，数量上限 20 ----
scan_one() { hub POST "/api/admin/servers/$SERVER_A/reality-scan" "$(jq -nc --arg t "$1" '{target:$t}')"; }
res="$(scan_one www.cloudflare.com:443)"; expect_code 200 "扫描一个公网目标"
printf '%s' "$res" | jq -e '.data | .target == "www.cloudflare.com:443" and (.feasible | type == "boolean") and (.serverNames | type == "array")
  and (.latencyMs | type == "number") and .privateTarget == false' >/dev/null || fail "扫描结果的形状不对：$res"
echo "  扫描 www.cloudflare.com:443：$(printf '%s' "$res" | jq -c '.data | {feasible, latencyMs, tlsVersion, reason}')"
scan_one 104.16.0.0/24 >/dev/null; expect_code 400 "扫描网段"
scan_one https://www.cloudflare.com >/dev/null; expect_code 400 "把网址当目标"
res="$(scan_one 127.0.0.1:443)"; expect_code 200 "扫描回环地址（面板不探测，只标记）"
printf '%s' "$res" | jq -e '.data | .privateTarget == true and .feasible == false' >/dev/null || fail "回环地址应标为内网目标且不可用：$res"
res="$(hub POST "/api/admin/servers/$SERVER_A/reality-targets" '{"targets":["www.cloudflare.com","www.microsoft.com:443"]}')"; expect_code 200 "探测一组候选"
[ "$(printf '%s' "$res" | jq '.data | length')" = "2" ] || fail "两个候选应返回两条结果：$res"
hub POST "/api/admin/servers/$SERVER_A/reality-targets" "$(jq -nc '{targets: [range(21) | "t\(.).example.com"]}')" >/dev/null; expect_code 400 "一次超过 20 个候选"
hub POST /api/admin/servers/999999/reality-scan '{"target":"www.cloudflare.com"}' >/dev/null; expect_code 404 "在不存在的服务器上扫描"
portal POST "/api/admin/servers/$SERVER_A/reality-scan" '{"target":"www.cloudflare.com"}' >/dev/null; expect_code 401 "终端用户扫描目标"

# ---- 12. 收尾：删除用户后面板上的平台条目清空 ----
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
hub DELETE "/api/admin/users/$OTHER_ID" >/dev/null; expect_code 200 "删除另一用户"
for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
  [ -z "$(hub_outbound "$TAG_1")" ] && break
  sleep 1
done
[ -z "$(hub_outbound "$TAG_1")" ] || fail "删除用户后面板上的平台出站应被清掉"
[ -n "$(hub_outbound "$BLOCK_TAG")" ] || fail "清掉链式条目时拦截规则应保留"
hub DELETE "/api/admin/chain-exits/$PLATFORM_ID" >/dev/null; expect_code 200 "删除平台出口"

# ---- 13. 关掉 routing.blockRule：下一轮从面板上撤掉拦截规则 ----
hub PUT /api/admin/settings '{"routing":{"blockRule":false}}' >/dev/null; expect_code 200 "关掉平台拦截规则"
for _ in $(seq 1 "$APPLY_WAIT_SECONDS"); do
  [ -z "$(hub_outbound "$BLOCK_TAG")" ] && break
  sleep 1
done
[ -z "$(hub_outbound "$BLOCK_TAG")" ] || fail "关掉设置后拦截出站应被撤掉"
[ "$(template | jq --arg t "$BLOCK_TAG" '[.routing.rules[] | select(.ruleTag == $t)] | length')" = "0" ] || fail "关掉设置后拦截路由应被撤掉"
hub PUT /api/admin/settings '{"routing":{"blockRule":true}}' >/dev/null; expect_code 200 "恢复平台拦截规则"
echo "IT-12 PASS"
