#!/usr/bin/env bash
# IT-16 服务器状态卡片（docs/08，docs/03 §5.1、§5.20，Ruling-83 – Ruling-86）：全新 xui-a 接入后
# → 列表与详情带类型化的资源快照（内存、磁盘总量非 0，Xray 在运行）、流量周期与默认账单
# → 账单：非法取值 400，保存后原样返回，校准后本周期已用等于填写值 → 趋势接口有采样点、带新字段，检测没开时不带延迟
# → 开启三网延迟检测：间隔小于 15 分钟 400；后台测一轮，探测请求是到面板公网 IPv4 的 ICMP、按电信 / 联通 / 移动三个 AS 各取一点、8 个包；
#   趋势与列表近况出现三家的延迟（模拟端让联通全丢）→ 未登录 401。
# 黑盒，只用 curl / jq / docker / python3（探测服务模拟端）。hub-server 不向真实的探测服务发任何请求（HUB_REACH_API_URL 指向本机）。
# 注意：会重建 xui-a 容器（不挂数据卷）；hub-server 用缩短了探测与检测周期的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
ROOT_USER=it16-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
CRED="it-$(openssl rand -hex 12)"
SERVER_NAME=it16-xui-a
REACH_PORT=18767        # 探测服务模拟端
ASN_TELECOM=4134
ASN_UNICOM=4837
ASN_MOBILE=9808
LATENCY_PACKETS=8
LATENCY_INTERVAL_MINUTES=15
CHECK_INTERVAL=2s
PROBE_INTERVAL=3s
ONLINE_WAIT_SECONDS=30
IP_WAIT_SECONDS=60      # 面板启动后要先去外部查一次公网 IP
LATENCY_WAIT_SECONDS=60 # 设置有 30 秒缓存，开启后最迟约半分钟才开始第一轮
POLL_SECONDS=2
GIB=1073741824
WORK="$(mktemp -d /tmp/it16.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-16: $*"; exit 1; }

# ---- 1. 全新 xui-a，签发一个 admin 范围的 API token ----
"${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes xui-a >/dev/null 2>&1 || fail "启动 xui-a 失败"
xui_token() {
  local jar="$WORK/xui.jar" user="" pw="" logs csrf
  for _ in $(seq 1 30); do
    logs="$("${COMPOSE[@]}" logs xui-a 2>/dev/null)"
    user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
    pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
    [ -n "$user" ] && curl -s -o /dev/null "$XUI_URL/csrf-token" && break
    sleep 1
  done
  [ -n "$user" ] || return 1
  csrf="$(curl -s -b "$jar" -c "$jar" "$XUI_URL/csrf-token" | jq -r .obj)"
  curl -s -b "$jar" -c "$jar" -X POST "$XUI_URL/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $csrf" \
    --data "$(jq -nc --arg u "$user" --arg p "$pw" '{username:$u,password:$p}')" | jq -e .success >/dev/null || return 1
  csrf="$(curl -s -b "$jar" -c "$jar" "$XUI_URL/csrf-token" | jq -r .obj)"
  curl -s -b "$jar" -c "$jar" -X POST "$XUI_URL/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
    -H "X-CSRF-Token: $csrf" --data '{"name":"it16","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
XUI_TOKEN="$(xui_token)"; [ -n "$XUI_TOKEN" ] || fail "xui-a 签发 token 失败"

# ---- 2. 探测服务模拟端：记下建测量的请求；按 AS 取点时每个 AS 回一个探测点，联通全丢，其余 130 ms 不丢包 ----
cat > "$WORK/latency_mock.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
LOG = sys.argv[2]
STORE = {}
LOSS_ASN = 4837
class Handler(BaseHTTPRequestHandler):
    def reply(self, status, payload):
        out = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(out)))
        self.end_headers()
        self.wfile.write(out)
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))) or b'{}')
        with open(LOG, 'a', encoding='utf-8') as f:
            f.write(json.dumps(body, ensure_ascii=False) + '\n')
        packets = int(body.get('measurementOptions', {}).get('packets', 3))
        results = []
        for loc in body.get('locations', []):
            asn = int(loc.get('asn', 0))
            lost = asn == LOSS_ASN
            stats = {'avg': None, 'rcv': 0, 'total': packets} if lost else {'avg': 130.4, 'rcv': packets, 'total': packets}
            results.append({'probe': {'country': loc.get('country', ''), 'city': 'City%d' % asn, 'network': 'Net', 'asn': asn},
                            'result': {'status': 'finished', 'stats': stats}})
        mid = 'm%d' % (len(STORE) + 1)
        STORE[mid] = results
        self.reply(202, {'id': mid, 'probesCount': len(results)})
    def do_GET(self):
        mid = self.path.rsplit('/', 1)[-1]
        if mid not in STORE:
            self.reply(404, {'error': {'type': 'not_found'}})
            return
        self.reply(200, {'status': 'finished', 'results': STORE[mid]})
    def log_message(self, *args):
        pass
HTTPServer(('127.0.0.1', int(sys.argv[1])), Handler).serve_forever()
PY
: > "$WORK/probe.log"
set -m
python3 -I "$WORK/latency_mock.py" "$REACH_PORT" "$WORK/probe.log" &
IT_PIDS+=($!)
set +m
wait_port "$REACH_PORT" 10 || fail "探测服务模拟端没起来"

# ---- 3. hub-server（探测 3 秒、检测 2 秒一轮、探测服务指向本机）与超管 ----
it_hub_config "$WORK/hub-server.yaml" -e "s/^  probeInterval: .*/  probeInterval: $PROBE_INTERVAL/" -e "s/^  checkInterval: .*/  checkInterval: $CHECK_INTERVAL/" || fail "派生配置失败"
grep -q "checkInterval: $CHECK_INTERVAL" "$WORK/hub-server.yaml" && grep -q "probeInterval: $PROBE_INTERVAL" "$WORK/hub-server.yaml" || fail "临时配置没有改到周期"
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml" HUB_REACH_API_URL="http://127.0.0.1:$REACH_PORT"
HUB_JAR="$WORK/hub.jar"
# call <cookie jar> <csrf> <method> <path> [json] → 打印响应体；状态码写入 $WORK/code
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body"
}
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }
expect_error() { expect_code "$1" "$3"; [ "$(jq -r .code "$WORK/body")" = "$2" ] || fail "$3：期望错误码 $2，实际 $(cat "$WORK/body")"; }
# 超管登录：首次登录当场绑定两步验证（R10）；登录即视为刚做过二次验证，联调配置把有效期拉长（common.sh it_admin_login）
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }

# ---- 4. 接入：列表与详情带资源快照、流量周期与默认账单 ----
SERVER_ID="$(hub POST /api/admin/servers "$(jq -nc --arg n "$SERVER_NAME" --arg u "$XUI_URL" --arg t "$XUI_TOKEN" \
  '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify",region:"US"}')" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
server() { hub GET "/api/admin/servers/$SERVER_ID"; }
deadline=$((SECONDS + ONLINE_WAIT_SECONDS))
until [ "$(server | jq -r .data.status)" = "online" ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "服务器应在线：$(cat "$WORK/body")"
  sleep 1
done
server >/dev/null
[ "$(jq -c '.data.resources | [(.memTotal > 0), (.diskTotal > 0), (.cpuCores > 0), .coreState, (.coreVersion != "")]' "$WORK/body")" = '[true,true,true,"running",true]' ] \
  || fail "资源快照应带内存、磁盘、核数与 Xray 状态：$(jq -c .data.resources "$WORK/body")"
[ "$(jq -c '.data.billing' "$WORK/body")" = '{"priceCents":null,"priceCurrency":"USD","billingCycle":"month","expiresOn":null,"trafficQuotaBytes":null,"trafficResetDay":1,"trafficMode":"both"}' ] \
  || fail "默认账单不对：$(jq -c .data.billing "$WORK/body")"
jq -e '.data.traffic | (.cycleStart | test("^[0-9]{4}-[0-9]{2}-01$")) and (.usedBytes >= 0)' "$WORK/body" >/dev/null || fail "流量周期应从 1 号起：$(jq -c .data.traffic "$WORK/body")"
jq -e 'has("statusSnapshot") | not' "$WORK/body" >/dev/null || fail "statusSnapshot 已换成 resources，不应再出现"
[ "$(hub GET /api/admin/servers | jq -r --argjson id "$SERVER_ID" '.data[] | select(.id == $id) | (.resources.memTotal > 0)')" = "true" ] || fail "列表也应带资源快照"

# ---- 5. 账单：非法取值 400；保存后原样返回；校准后本周期已用等于填写值 ----
billing_of() { jq -nc --arg c "$1" --argjson d "$2" --argjson used "$3" \
  '{priceCents:4990,priceCurrency:$c,billingCycle:"year",expiresOn:"2099-01-01",trafficQuotaBytes:(1024*'"$GIB"'),trafficResetDay:$d,trafficMode:"both",trafficUsedBytes:$used}'; }
hub PUT "/api/admin/servers/$SERVER_ID/billing" "$(billing_of usd 1 null)" >/dev/null; expect_error 400 invalid_argument "币种小写"
hub PUT "/api/admin/servers/$SERVER_ID/billing" "$(billing_of USD 32 null)" >/dev/null; expect_error 400 invalid_argument "重置日 32"
hub PUT "/api/admin/servers/$SERVER_ID/billing" "$(billing_of USD 1 null | jq -c '.expiresOn = "2099/01/01"')" >/dev/null; expect_error 400 invalid_argument "到期日格式"
USED=$((173 * GIB))
hub PUT "/api/admin/servers/$SERVER_ID/billing" "$(billing_of USD 1 "$USED")" >/dev/null; expect_code 200 "保存账单"
[ "$(jq -c '[.data.billing.priceCents, .data.billing.expiresOn, .data.billing.billingCycle, .data.traffic.usedBytes]' "$WORK/body")" = "[4990,\"2099-01-01\",\"year\",$USED]" ] \
  || fail "保存后应原样返回且已用等于校准值：$(jq -c '[.data.billing, .data.traffic]' "$WORK/body")"
hub PUT "/api/admin/servers/999999/billing" "$(billing_of USD 1 null)" >/dev/null; expect_error 404 not_found "不存在的服务器"

# ---- 6. 趋势：有采样点、带新字段；检测没开时不带延迟 ----
# 采样由后台探测写（接入时那次探测不记），等第一条
deadline=$((SECONDS + ONLINE_WAIT_SECONDS))
until [ "$(hub GET "/api/admin/servers/$SERVER_ID/metrics?range=24h" | jq -r '.data.points | length')" -ge 1 ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${ONLINE_WAIT_SECONDS}s 内没有采样：$(cat "$WORK/body")"
  sleep 1
done
expect_code 200 "24 小时趋势"
jq -e '(.data.points | length) >= 1 and (.data.points[-1].memTotal > 0) and (.data.latency == null)' "$WORK/body" >/dev/null \
  || fail "24 小时趋势应有带内存的采样点、不带延迟：$(cat "$WORK/body")"
hub GET "/api/admin/servers/$SERVER_ID/metrics?range=1h" >/dev/null; expect_error 400 invalid_argument "趋势范围不合法"
hub GET "/api/admin/servers/trends" >/dev/null; expect_code 200 "列表近况"
jq -e --argjson id "$SERVER_ID" '[.data[] | select(.serverId == $id)] | length == 1 and (.[0].latency == null) and (.[0].net | length >= 1)' "$WORK/body" >/dev/null \
  || fail "列表近况应有速率点、不带延迟：$(cat "$WORK/body")"
[ ! -s "$WORK/probe.log" ] || fail "检测没开时不应向探测服务发请求：$(cat "$WORK/probe.log")"

# ---- 7. 开启三网延迟检测：后台测一轮，请求按三个 AS 取点；趋势与近况出现三家延迟，联通全丢 ----
deadline=$((SECONDS + IP_WAIT_SECONDS))
until [ -n "$(server | jq -r '.data.publicIpv4')" ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "面板没有报出公网 IPv4，无法测延迟：$(jq -c '[.data.publicIpv4, .data.publicIpv6]' "$WORK/body")"
  sleep "$POLL_SECONDS"
done
PUBLIC_IP="$(server | jq -r .data.publicIpv4)"
hub PUT /api/admin/settings '{"latency":{"intervalMinutes":5}}' >/dev/null; expect_error 400 invalid_argument "延迟检测间隔小于 15 分钟"
hub PUT /api/admin/settings "$(jq -nc --argjson m "$LATENCY_INTERVAL_MINUTES" '{latency:{enabled:true,intervalMinutes:$m}}')" >/dev/null
expect_code 200 "开启三网延迟检测"
[ "$(jq -c '.data.latency' "$WORK/body")" = "{\"enabled\":true,\"intervalMinutes\":$LATENCY_INTERVAL_MINUTES}" ] || fail "设置应返回延迟检测分组：$(jq -c .data.latency "$WORK/body")"
latency_of() { hub GET "/api/admin/servers/$SERVER_ID/metrics?range=24h" | jq -c '.data.latency'; }
deadline=$((SECONDS + LATENCY_WAIT_SECONDS))
until [ "$(latency_of | jq -r '(.telecom // []) | length')" = "1" ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${LATENCY_WAIT_SECONDS}s 内后台没有测完：$(latency_of)，模拟端请求 $(cat "$WORK/probe.log")"
  sleep "$POLL_SECONDS"
done
[ "$(jq -s 'length' "$WORK/probe.log")" = "1" ] || fail "间隔内同一台服务器只测一次：$(cat "$WORK/probe.log")"
[ "$(jq -s -c --arg ip "$PUBLIC_IP" '.[0] | [(.target == $ip), .type, .measurementOptions.protocol, .measurementOptions.packets, ([.locations[] | "\(.country)/\(.asn)/\(.limit)"] | join(","))]' "$WORK/probe.log")" \
  = "[true,\"ping\",\"ICMP\",$LATENCY_PACKETS,\"CN/$ASN_TELECOM/1,CN/$ASN_UNICOM/1,CN/$ASN_MOBILE/1\"]" ] \
  || fail "探测请求应是到面板公网 IPv4 的 ICMP、按三个 AS 各取一点：$(cat "$WORK/probe.log")"
[ "$(latency_of | jq -c '[.telecom[0].avgMs, .telecom[0].lossPercent, .unicom[0].avgMs, .unicom[0].lossPercent, .mobile[0].avgMs]')" = '[130,0,null,100,130]' ] \
  || fail "三家延迟不对：$(latency_of)"
hub GET "/api/admin/servers/trends" >/dev/null
jq -e --argjson id "$SERVER_ID" '.data[] | select(.serverId == $id) | (.latency.unicom | length) == 1' "$WORK/body" >/dev/null \
  || fail "列表近况应带延迟：$(cat "$WORK/body")"

# ---- 8. 未登录 401 ----
call "$WORK/anon.jar" "" GET /api/admin/servers/trends >/dev/null; expect_error 401 unauthenticated "未登录看近况"
call "$WORK/anon.jar" "" GET "/api/admin/servers/$SERVER_ID/metrics" >/dev/null; expect_error 401 unauthenticated "未登录看趋势"

echo "IT-16 通过"
