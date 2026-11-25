#!/usr/bin/env bash
# IT-5 订阅与客户后台（docs/08，M3 DoD）：全新 3x-ui → 接入两个入站（VMess+WS、VLESS+Reality）→ 节点组 + 套餐 + 用户
# → 终端用户登录客户后台 → 取订阅地址 → 四种格式分别用 base64 解码 / mihomo / sing-box / xray 校验
# → 重置订阅后旧地址 404、连接凭据已轮换 → 自设连接密码与 SS2022 密钥（Ruling-58、Ruling-60）→ 停用后订阅只剩说明节点、后台接口 403
# → 订阅路径前缀（Ruling-59）。黑盒，只用 curl / jq / docker。
# 注意：会重建 xui-a 容器（不挂数据卷，见 docker-compose.yml），不影响 PostgreSQL 与 MinIO。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
ROOT_USER=it5-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it5-user
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
BAD_CRED="it-$(openssl rand -hex 12)"
GIB=1073741824
WS_PORT=24701
REALITY_PORT=24702
SS_PORT=24703
NODE_COUNT=3       # VMess、VLESS、SS2022 各一个直连节点
UUID_NODE_COUNT=2  # 链接里带 UUID 的节点（VMess、VLESS）
CONN_PASSWORD="it5-Pass_$(openssl rand -hex 4)"
NEW_SUB_PATH="it5path$(openssl rand -hex 4)"
LEGACY_SUB_PATH=sub
FORMAT_COUNT=4
ROTATE_WAIT_SECONDS=20
SINGBOX_IMAGE="${IT_SINGBOX_IMAGE:-ghcr.io/sagernet/sing-box:latest}"
# 配置文件要挂进校验容器；macOS 上 mktemp 的默认目录不在 docker 的共享目录里，所以固定放 /tmp
WORK="$(mktemp -d /tmp/it5.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-5: $*"; exit 1; }

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
  -H "X-CSRF-Token: $(xui_csrf)" --data '{"name":"it5","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty')"
[ -n "$XUI_TOKEN" ] || fail "签发 3x-ui API token 失败"

# ---- 2. hub-server 与超管 ----
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
it_hub_config "$WORK/hub-server.yaml" || fail "派生配置失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml"
HUB_JAR="$WORK/hub.jar"
# login_body_of <用户名> <密码>
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
# 超管登录：首次登录当场绑定两步验证（R10）；登录即视为刚做过二次验证，联调配置把有效期拉长（common.sh it_admin_login）
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "hub 登录失败"
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

# ---- 3. 接入服务器与两个入站，节点放进节点组，建套餐与用户 ----
server="$(hub POST /api/admin/servers "$(jq -nc --arg u "$XUI_URL" --arg t "$XUI_TOKEN" '{name:"it5-a",providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')")"
expect_code 200 "接入服务器"
SERVER_ID="$(printf '%s' "$server" | jq -r .data.id)"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$(jq -nc --argjson p "$WS_PORT" '{remark:"it5-ws",port:$p,template:"vmess-ws",templateParams:{path:"/it5"}}')" >/dev/null
expect_code 200 "新建 VMess 入站"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" \
  "$(jq -nc --argjson p "$REALITY_PORT" '{remark:"it5-reality",port:$p,template:"vless-reality",templateParams:{target:"www.cloudflare.com:443"}}')" >/dev/null
expect_code 200 "新建 Reality 入站"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$(jq -nc --argjson p "$SS_PORT" '{remark:"it5-ss",port:$p,template:"shadowsocks-2022"}')" >/dev/null
expect_code 200 "新建 Shadowsocks 入站"
node_ids="$(hub GET "/api/admin/nodes?pageSize=100" | jq -c --argjson s "$SERVER_ID" '[.data.items[] | select(.serverId == $s) | .id]')"
[ "$(printf '%s' "$node_ids" | jq length)" = "$NODE_COUNT" ] || fail "应有 $NODE_COUNT 个直连节点：$node_ids"
group="$(hub POST /api/admin/node-groups "$(jq -nc --argjson ids "$node_ids" '{name:"it5-group",nodeIds:$ids}')")"; expect_code 200 "新建节点组"
plan="$(hub POST /api/admin/plans "$(jq -nc --argjson g "$(printf '%s' "$group" | jq .data.id)" --argjson l "$GIB" '{name:"it5-plan",trafficLimit:$l,durationDays:30,nodeGroupIds:[$g]}')")"
expect_code 200 "新建套餐"
user="$(hub POST /api/admin/users "$(login_body_of "$END_USER" "$USER_CRED" | jq -c --argjson plan "$(printf '%s' "$plan" | jq .data.id)" '. + {planId:$plan}')")"
expect_code 200 "新建用户"
USER_ID="$(printf '%s' "$user" | jq -r .data.id)"
hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null; expect_code 200 "对账"

# ---- 4. 终端用户登录客户后台（docs/03 §5.3）：密码错 401，未登录 401，登录后只凭会话取本人数据 ----
USER_JAR="$WORK/user.jar"
call "$USER_JAR" "" POST /api/user/auth/login "$(login_body_of "$END_USER" "$BAD_CRED")" >/dev/null; expect_code 401 "密码错误登录"
call "$USER_JAR" "" GET /api/user/subscription >/dev/null; expect_code 401 "未登录取订阅"
USER_CSRF="$(call "$USER_JAR" "" POST /api/user/auth/login "$(login_body_of "$END_USER" "$USER_CRED")" | jq -r '.data.csrfToken // empty')"
[ -n "$USER_CSRF" ] || fail "终端用户登录失败：$(cat "$WORK/body")"
portal() { call "$USER_JAR" "$USER_CSRF" "$@"; }
[ "$(portal GET /api/user/nodes | jq '[.data[].nodes[]] | length')" = "$NODE_COUNT" ] || fail "客户后台应看到 $NODE_COUNT 个节点：$(cat "$WORK/body")"
sub="$(portal GET /api/user/subscription)"; expect_code 200 "取订阅地址"
SUB_URL="$(printf '%s' "$sub" | jq -r .data.url)"
[ "$(printf '%s' "$sub" | jq -r '[.data.formats[].format] | sort | join(",")')" = "base64,mihomo,singbox,xray" ] || fail "应给出四种格式：$sub"

# ---- 5. 四种格式（docs/03 §5.4） ----
# fetch <格式> <文件>：拉订阅正文到文件，响应头写入 $WORK/headers，打印状态码
fetch() { curl -s -D "$WORK/headers" -o "$2" -w '%{http_code}' "$SUB_URL?format=$1"; }
# b64d：标准输入的 Base64 解码到标准输出；用 jq 是因为分享链接常省略填充，各平台的 base64 命令对此行为不一
b64d() { jq -Rrs 'gsub("\\s"; "") | @base64d'; }
# link_ids <base64 订阅文件>：各节点链接里的连接凭据（vmess 的 id、vless 的 userinfo），排序后一行一个
link_ids() {
  b64d < "$1" | while read -r link; do
    case "$link" in
      vmess://*) printf '%s' "${link#vmess://}" | b64d | jq -r .id ;;
      vless://*) link="${link#vless://}"; printf '%s\n' "${link%%@*}" ;;
      *) ;;  # 信息节点等其它链接不带用户凭据
    esac
  done | sort
}
# id_count <base64 订阅文件>：带 UUID 的节点数
id_count() { link_ids "$1" | grep -c . || true; }
# ss_links <base64 订阅文件>：Shadowsocks 链接（含用户密钥），排序后一行一个
ss_links() { b64d < "$1" | grep '^ss://' | sort || true; }
[ "$(fetch base64 "$WORK/sub.b64")" = "200" ] || fail "base64 订阅应为 200"
grep -qi '^subscription-userinfo: .*total=' "$WORK/headers" || fail "缺 Subscription-Userinfo 响应头：$(cat "$WORK/headers")"
grep -qi '^profile-update-interval:' "$WORK/headers" || fail "缺 Profile-Update-Interval 响应头"
IDS_BEFORE="$(link_ids "$WORK/sub.b64")"
[ "$(id_count "$WORK/sub.b64")" = "$UUID_NODE_COUNT" ] || fail "base64 订阅应有 $UUID_NODE_COUNT 个带 UUID 的节点：$(b64d < "$WORK/sub.b64")"
[ -n "$(ss_links "$WORK/sub.b64")" ] || fail "base64 订阅应有 Shadowsocks 节点：$(b64d < "$WORK/sub.b64")"

mkdir -p "$WORK/cfg"
[ "$(fetch mihomo "$WORK/cfg/mihomo.yaml")" = "200" ] || fail "mihomo 订阅应为 200"
[ "$(fetch singbox "$WORK/cfg/singbox.json")" = "200" ] || fail "sing-box 订阅应为 200"
[ "$(fetch xray "$WORK/cfg/xray.json")" = "200" ] || fail "xray 订阅应为 200"
chmod -R a+rX "$WORK"
mihomo_test "$WORK/cfg/mihomo.yaml" "$WORK/mihomo.log" "$WORK/mihomo-home" || fail "mihomo -t 未通过：$(tail -5 "$WORK/mihomo.log")"
docker run --rm -v "$WORK/cfg:/cfg:ro" "$SINGBOX_IMAGE" check -c /cfg/singbox.json >"$WORK/singbox.log" 2>&1 || fail "sing-box check 未通过：$(tail -5 "$WORK/singbox.log")"
# Xray JSON 是“每个节点一份完整配置”的数组，逐份经标准输入交给面板容器里的 xray 校验；
# 必须写 -c stdin:，否则 xray 会去读工作目录里面板自己的 config.json，校验形同虚设
XRAY_CONFIGS="$(jq length "$WORK/cfg/xray.json")"
[ "$XRAY_CONFIGS" -ge "$NODE_COUNT" ] || fail "xray 订阅应至少有 $NODE_COUNT 份配置"
XRAY_BIN="$("${COMPOSE[@]}" exec -T xui-a sh -c 'ls /app/bin/xray-linux-* | head -1' | tr -d '\r')"
[ -n "$XRAY_BIN" ] || fail "面板容器里没有找到 xray"
for i in $(seq 0 $((XRAY_CONFIGS - 1))); do
  jq ".[$i]" "$WORK/cfg/xray.json" | "${COMPOSE[@]}" exec -T -w /app/bin xui-a "$XRAY_BIN" run -test -c stdin: -format json >"$WORK/xray.log" 2>&1 \
    || fail "xray -test 未通过（第 $i 份）：$(tail -3 "$WORK/xray.log")"
done

[ "$(curl -s -o /dev/null -w '%{http_code}' "$SUB_URL?format=nope")" = "400" ] || fail "不认识的 format 应为 400"
[ "$(portal GET "/api/user/subscribe-logs?pageSize=100" | jq '.data.items | length')" -ge "$FORMAT_COUNT" ] || fail "订阅记录应至少 $FORMAT_COUNT 条：$(cat "$WORK/body")"

# ---- 6. 重置订阅：旧地址 404，新地址可用，连接凭据已轮换 ----
call "$USER_JAR" "" POST /api/user/subscription/reset >/dev/null; expect_code 403 "不带 CSRF 头重置订阅"
OLD_URL="$SUB_URL"
SUB_URL="$(portal POST /api/user/subscription/reset | jq -r '.data.url // empty')"; expect_code 200 "重置订阅"
[ -n "$SUB_URL" ] && [ "$SUB_URL" != "$OLD_URL" ] || fail "重置后订阅地址应变化"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$OLD_URL")" = "404" ] || fail "旧订阅地址应为 404"
rotated=""
for _ in $(seq 1 "$ROTATE_WAIT_SECONDS"); do
  hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null
  # 两个节点的凭据都换了才算轮换完成：新旧两组没有交集
  if [ "$(fetch base64 "$WORK/sub2.b64")" = "200" ] && [ "$(id_count "$WORK/sub2.b64")" = "$UUID_NODE_COUNT" ] \
    && [ -z "$(comm -12 <(printf '%s\n' "$IDS_BEFORE") <(link_ids "$WORK/sub2.b64"))" ]; then
    rotated=1
    break
  fi
  sleep 1
done
[ -n "$rotated" ] || fail "重置后 ${ROTATE_WAIT_SECONDS}s 内连接凭据未轮换完：$(b64d < "$WORK/sub2.b64")"

# ---- 6b. 自设连接密码（Ruling-58、Ruling-60）：写法不对 400；有 SS2022 的服务器沿用平台密钥；清空恢复随机 ----
IDS_ROTATED="$(link_ids "$WORK/sub2.b64")"
SS_ROTATED="$(ss_links "$WORK/sub2.b64")"
[ "$(portal GET /api/user/subscription | jq -r .data.hasCustomPassword)" = "false" ] || fail "默认不应有自设密码：$(cat "$WORK/body")"
portal PUT /api/user/subscription/password '{"password":"bad pass"}' >/dev/null; expect_code 400 "连接密码含空格"
portal PUT /api/user/subscription/password '{"password":"short"}' >/dev/null; expect_code 400 "连接密码太短"
[ "$(portal PUT /api/user/subscription/password "$(jq -nc --arg p "$CONN_PASSWORD" '{password:$p}')" | jq -r .data.hasCustomPassword)" = "true" ] \
  || fail "设密码后应标记为自设：$(cat "$WORK/body")"
[ "$(portal GET /api/user/subscription | jq -r .data.url)" = "$SUB_URL" ] || fail "设连接密码不应改变订阅地址"
# 这台服务器上有 SS2022 入站，password 一律写平台的 SS2022 密钥（Ruling-60）：对账后 SS2022 链接与 UUID 都不变
hub POST "/api/admin/users/$USER_ID/reconcile" >/dev/null; expect_code 200 "设密码后对账"
[ "$(fetch base64 "$WORK/sub5.b64")" = "200" ] || fail "设密码后订阅应为 200"
[ "$(ss_links "$WORK/sub5.b64")" = "$SS_ROTATED" ] || fail "有 SS2022 的服务器上自设密码不应改动 SS2022 密钥：$(b64d < "$WORK/sub5.b64")"
[ "$(link_ids "$WORK/sub5.b64")" = "$IDS_ROTATED" ] || fail "设连接密码不应改动 UUID"
[ "$(portal PUT /api/user/subscription/password '{"password":""}' | jq -r .data.hasCustomPassword)" = "false" ] || fail "清空后应恢复随机：$(cat "$WORK/body")"

# ---- 7. 停用：订阅仍 200 但没有可用节点，客户后台 403 account_disabled；恢复后回来 ----
hub PUT "/api/admin/users/$USER_ID" '{"remark":"","isDisabled":true}' >/dev/null; expect_code 200 "停用用户"
[ "$(fetch base64 "$WORK/sub3.b64")" = "200" ] || fail "停用后订阅仍应为 200"
[ "$(id_count "$WORK/sub3.b64")" = "0" ] || fail "停用后订阅不应再有带凭据的节点：$(b64d < "$WORK/sub3.b64")"
[ "$(portal GET /api/user/subscription | jq -r .code)" = "account_disabled" ] || fail "停用后客户后台应返回 account_disabled：$(cat "$WORK/body")"
expect_code 403 "停用后取订阅地址"
portal GET /api/user/auth/me >/dev/null; expect_code 200 "停用后仍可查看本人会话"
hub PUT "/api/admin/users/$USER_ID" '{"remark":"","isDisabled":false}' >/dev/null; expect_code 200 "启用用户"
[ "$(fetch base64 "$WORK/sub4.b64")" = "200" ] || fail "启用后订阅应为 200"
[ "$(id_count "$WORK/sub4.b64")" = "$UUID_NODE_COUNT" ] || fail "启用后订阅应恢复 $UUID_NODE_COUNT 个带 UUID 的节点"

# ---- 7b. 订阅路径（Ruling-59）：随机前缀；全新库的旧前缀是 sub，过渡期内可用；改前缀且过渡期为 0 时旧前缀立即 404 ----
SUB_PATH="$(printf '%s' "$SUB_URL" | sed -E 's#^https?://[^/]+/([^/]+)/.*#\1#')"
SUB_TOKEN="${SUB_URL##*/}"
[ "$SUB_PATH" != "$LEGACY_SUB_PATH" ] && printf '%s' "$SUB_PATH" | grep -Eq '^[a-z0-9]{10,32}$' || fail "订阅前缀应是随机串：$SUB_URL"
settings="$(hub GET /api/admin/settings)"
[ "$(printf '%s' "$settings" | jq -r '.data.subscription.path + "," + .data.subscription.oldPath')" = "$SUB_PATH,$LEGACY_SUB_PATH" ] || fail "设置里的前缀不符：$settings"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$LEGACY_SUB_PATH/$SUB_TOKEN")" = "200" ] || fail "过渡期内旧前缀 /sub/ 应可用"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/zzzzzzzzzz9/$SUB_TOKEN")" = "404" ] || fail "不认识的前缀应为 404"
hub PUT /api/admin/settings '{"subscription":{"path":"Bad_Path"}}' >/dev/null; expect_code 400 "前缀写法不对"
hub PUT /api/admin/settings "$(jq -nc --arg p "$NEW_SUB_PATH" '{subscription:{path:$p,pathGraceDays:0}}')" >/dev/null; expect_code 200 "改订阅前缀"
NEW_URL="$(portal GET /api/user/subscription | jq -r .data.url)"
[ "$NEW_URL" = "$BASE/$NEW_SUB_PATH/$SUB_TOKEN" ] || fail "订阅地址应换成新前缀：$NEW_URL"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$NEW_URL")" = "200" ] || fail "新前缀应可用"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SUB_URL")" = "404" ] || fail "过渡期为 0 时旧前缀应立即 404"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$LEGACY_SUB_PATH/$SUB_TOKEN")" = "404" ] || fail "更早的旧前缀应失效"
SUB_URL="$NEW_URL"

# ---- 8. 清理：删除用户后订阅地址 404 ----
hub DELETE "/api/admin/users/$USER_ID" >/dev/null; expect_code 200 "删除用户"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$SUB_URL")" = "404" ] || fail "删除用户后订阅地址应为 404"
echo "IT-5 PASS"
