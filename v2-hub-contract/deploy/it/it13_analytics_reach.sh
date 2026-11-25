#!/usr/bin/env bash
# IT-13 总览分析与大陆连通检测（docs/08，docs/03 §5.20，Ruling-66、Ruling-67）：全新 xui-a 一个入站，入站上加一个公网域名的对外地址
# → 检测没开时节点不带结论、详情 403 → 开启后后台自动测一轮：对外地址的目标判为正常，面板地址是回环地址、不发给探测服务
# → 模拟端把该端口改成“大陆不通、境外通”，手动检测两次（连续 2 轮）→ 发件箱出现被墙告警、总览与服务器列表标出、审计有记录
# → 改回连通，手动检测一次 → 发件箱出现恢复通知 → 同一管理员一分钟内第 7 次手动检测 429
# → 总览各分析分区：时间范围只接受 7 / 30 / 90，用户累计数与用户列表一致，流量、财务、待处理各块的形状正确，未登录 401。
# 黑盒，只用 curl / jq / docker / python3（探测服务模拟端）。hub-server 不向真实的探测服务发任何请求（HUB_REACH_API_URL 指向本机）。
# 注意：会重建 xui-a 容器（不挂数据卷）；hub-server 用缩短了检测周期、关掉分析缓存的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
ROOT_USER=it13-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it13-user
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
SITE_NAME="IT13 站点"
SERVER_NAME=it13-xui-a
MAIL_DOMAIN=it13.example.com
ALERT_MAIL="ops@$MAIL_DOMAIN"
NODE_HOST=node.it13.example.com   # 对外地址：公网域名，只交给本机模拟端
INBOUND_PORT=24013
SUBJECT_BLOCKED="【${SITE_NAME}】节点 $NODE_HOST:$INBOUND_PORT 大陆连不上（疑似被墙）"
SUBJECT_RECOVERED="【${SITE_NAME}】节点 $NODE_HOST:$INBOUND_PORT 大陆已恢复连通"
SMTP_HOST=127.0.0.1
SMTP_DEAD_PORT=1        # 没有服务监听，连接立即被拒
REACH_PORT=18766        # 探测服务模拟端
REACH_INTERVAL_MINUTES=10
MANUAL_LIMIT=6          # 每个管理员每分钟的手动检测次数（docs/03 §5.20）
CHECK_INTERVAL=2s
PROBE_INTERVAL=3s
AUTO_WAIT_SECONDS=60    # 设置有 30 秒缓存，开启后最迟约半分钟才开始第一轮
NOTIFY_WAIT_SECONDS=30
POLL_SECONDS=2
OUTBOX_PAGE_SIZE=100
AUDIT_PAGE_SIZE=100
WORK="$(mktemp -d /tmp/it13.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-13: $*"; exit 1; }

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
    -H "X-CSRF-Token: $csrf" --data '{"name":"it13","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
XUI_TOKEN="$(xui_token)"; [ -n "$XUI_TOKEN" ] || fail "xui-a 签发 token 失败"

# ---- 2. 探测服务模拟端：建测量时记下请求；blocked 文件里列出的端口“大陆不通、境外通”，ICMP 一律连通 ----
cat > "$WORK/reach_mock.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
LOG, BLOCKED = sys.argv[2], sys.argv[3]
STORE = {}
def blocked_ports():
    try:
        with open(BLOCKED, encoding='utf-8') as f:
            return {line.strip() for line in f if line.strip()}
    except FileNotFoundError:
        return set()
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
        opts = body.get('measurementOptions', {})
        is_blocked = opts.get('protocol') == 'TCP' and str(opts.get('port')) in blocked_ports()
        results = []
        for loc in body.get('locations', []):
            country = loc.get('country', '')
            is_ok = not (is_blocked and country == 'CN')
            for i in range(int(loc.get('limit', 1))):
                stats = {'avg': 20.5, 'rcv': 3} if is_ok else {'avg': None, 'rcv': 0}
                results.append({'probe': {'country': country, 'city': 'City%d' % i, 'network': 'Net%d' % i},
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
: > "$WORK/reach.log"; : > "$WORK/blocked"
set -m
python3 -I "$WORK/reach_mock.py" "$REACH_PORT" "$WORK/reach.log" "$WORK/blocked" &
IT_PIDS+=($!)
set +m
wait_port "$REACH_PORT" 10 || fail "探测服务模拟端没起来"
# probe_count <目标>：模拟端收到的、探测该目标的请求数
probe_count() { jq -s --arg t "$1" '[.[] | select(.target == $t)] | length' "$WORK/reach.log"; }

# ---- 3. hub-server（检测 2 秒一轮、分析不缓存、探测服务指向本机）与超管 ----
it_hub_config "$WORK/hub-server.yaml" -e "s/^  probeInterval: .*/  probeInterval: $PROBE_INTERVAL/" -e "s/^  checkInterval: .*/  checkInterval: $CHECK_INTERVAL/" \
  -e 's/^  cacheTtl: .*/  cacheTtl: 0s/' || fail "派生配置失败"
grep -q "checkInterval: $CHECK_INTERVAL" "$WORK/hub-server.yaml" && grep -q 'cacheTtl: 0s' "$WORK/hub-server.yaml" || fail "临时配置没有改到检测周期或分析缓存"
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml" HUB_REACH_API_URL="http://127.0.0.1:$REACH_PORT" \
  HUB_SMTP_HOST="$SMTP_HOST" HUB_SMTP_PORT="$SMTP_DEAD_PORT" HUB_SMTP_FROM="noreply@$MAIL_DOMAIN" HUB_SMTP_TLS=none
HUB_JAR="$WORK/hub.jar"
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
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
outbox_count() {
  hub GET "/api/admin/mail-outbox?pageSize=$OUTBOX_PAGE_SIZE" | jq --arg r "$ALERT_MAIL" --arg s "$1" '[.data.items[] | select(.recipient == $r and .subject == $s)] | length'
}

# ---- 4. 接入服务器、建入站、加公网域名的对外地址 ----
hub PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" --arg m "$ALERT_MAIL" '{site:{name:$n},alert:{emails:[$m]}}')" >/dev/null
expect_code 200 "保存站点与告警邮箱"
[ "$(jq -c '[.data.reach.enabled, .data.reach.intervalMinutes, .data.reach.notify, .data.reach.hasToken]' "$WORK/body")" = "[false,30,true,false]" ] \
  || fail "大陆连通检测默认应关、间隔 30 分钟、通知开、没有令牌：$(jq -c .data.reach "$WORK/body")"
SERVER_ID="$(hub POST /api/admin/servers "$(jq -nc --arg n "$SERVER_NAME" --arg u "$XUI_URL" --arg t "$XUI_TOKEN" \
  '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
hub POST "/api/admin/servers/$SERVER_ID/inbounds" "$(jq -nc --argjson p "$INBOUND_PORT" '{remark:"it13-ws",port:$p,template:"vmess-ws",templateParams:{path:"/it13"}}')" >/dev/null
expect_code 200 "新建入站"
INBOUND_ID="$(hub GET "/api/admin/servers/$SERVER_ID/inbounds" | jq -r '.data[0].id')"
hub POST "/api/admin/servers/$SERVER_ID/endpoints" "$(jq -nc --argjson id "$INBOUND_ID" --arg h "$NODE_HOST" '{inboundIds:[$id],addresses:[$h],port:0}')" >/dev/null
expect_code 200 "添加对外地址"
server_status() { hub GET "/api/admin/servers/$SERVER_ID" | jq -r .data.status; }
for _ in $(seq 1 20); do [ "$(server_status)" = "online" ] && break; sleep 1; done
[ "$(server_status)" = "online" ] || fail "服务器应在线：$(cat "$WORK/body")"
nodes() { hub GET "/api/admin/nodes?serverId=$SERVER_ID&pageSize=100"; }
HOST_NODE="$(nodes | jq -r --arg h "$NODE_HOST" '.data.items[] | select(.address == $h) | .id')"
DIRECT_NODE="$(nodes | jq -r '.data.items[] | select(.endpointId == null) | .id')"
[ -n "$HOST_NODE" ] && [ -n "$DIRECT_NODE" ] || fail "应有直连节点与 Host 节点：$(cat "$WORK/body")"
node_reach() { nodes | jq -c --argjson id "$1" '.data.items[] | select(.id == $id) | .reach'; }

# ---- 5. 检测没开：节点与服务器不带结论，详情与手动检测 403，模拟端没有收到请求 ----
[ "$(node_reach "$HOST_NODE")" = "null" ] || fail "检测没开时节点不应带结论：$(node_reach "$HOST_NODE")"
[ "$(hub GET "/api/admin/servers/$SERVER_ID" | jq -c .data.reach)" = "null" ] || fail "检测没开时服务器不应带结论"
hub GET "/api/admin/nodes/$HOST_NODE/reach" >/dev/null; expect_error 403 feature_disabled "检测没开时看详情"
hub POST "/api/admin/nodes/$HOST_NODE/reach/check" >/dev/null; expect_error 403 feature_disabled "检测没开时手动检测"
[ "$(hub GET /api/admin/analytics/servers | jq -c .data.reach)" = "null" ] || fail "检测没开时总览不带大陆连通汇总"
[ ! -s "$WORK/reach.log" ] || fail "检测没开时不应向探测服务发请求：$(cat "$WORK/reach.log")"

# ---- 6. 开启：后台自动测一轮；公网域名的目标正常，回环地址的目标不发出去 ----
hub PUT /api/admin/settings "$(jq -nc --argjson m "$REACH_INTERVAL_MINUTES" '{reach:{enabled:true,intervalMinutes:$m}}')" >/dev/null
expect_code 200 "开启大陆连通检测"
hub PUT /api/admin/settings '{"reach":{"intervalMinutes":5}}' >/dev/null; expect_error 400 invalid_argument "检测间隔小于 10 分钟"
deadline=$((SECONDS + AUTO_WAIT_SECONDS))
until [ "$(node_reach "$HOST_NODE" | jq -r '.verdict // empty')" = "ok" ] && [ "$(node_reach "$DIRECT_NODE" | jq -r '.verdict // empty')" = "unknown" ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${AUTO_WAIT_SECONDS}s 内后台没有测完：Host $(node_reach "$HOST_NODE")，直连 $(node_reach "$DIRECT_NODE")"
  sleep "$POLL_SECONDS"
done
[ "$(node_reach "$HOST_NODE" | jq -c '[.scope, .cnOk, .cnTotal]')" = '["port",3,3]' ] || fail "Host 节点应为 3 / 3 连通：$(node_reach "$HOST_NODE")"
[ "$(probe_count "$NODE_HOST")" = "1" ] || fail "间隔内同一目标只测一次：$(cat "$WORK/reach.log")"
[ "$(jq -s '[.[] | select(.target != "'"$NODE_HOST"'")] | length' "$WORK/reach.log")" = "0" ] || fail "回环地址不应发给探测服务：$(cat "$WORK/reach.log")"
[ "$(jq -s -c '.[0] | [.type, .measurementOptions.protocol, .measurementOptions.port, ([.locations[] | .country] | join(","))]' "$WORK/reach.log")" \
  = "[\"ping\",\"TCP\",$INBOUND_PORT,\"CN,JP,US\"]" ] || fail "探测请求应是到入站端口的 TCP ping、大陆加日美对照：$(cat "$WORK/reach.log")"
hub GET "/api/admin/nodes/$DIRECT_NODE/reach" >/dev/null; expect_code 200 "直连节点详情"
[ "$(jq -r '.data.lastError' "$WORK/body")" != "" ] || fail "非公网地址应说明原因：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/servers/$SERVER_ID" | jq -c .data.reach)" = '{"total":2,"ok":1,"blocked":0,"partial":0}' ] || fail "服务器应有 2 个检测目标、其中 1 个正常：$(cat "$WORK/body")"

# ---- 7. 被墙：手动检测两次（连续 2 轮）→ 告警一次、总览与列表标出、审计有记录 ----
echo "$INBOUND_PORT" > "$WORK/blocked"
check() { hub POST "/api/admin/nodes/$HOST_NODE/reach/check"; }
check >/dev/null; expect_code 200 "手动检测（被墙第 1 轮）"
[ "$(jq -c '[.data.verdict, .data.cnOk, .data.cnTotal, .data.controlOk, .data.controlTotal, .data.ipReachable, (.data.probes | length)]' "$WORK/body")" \
  = '["blocked",0,3,2,2,true,5]' ] || fail "应判为被墙且 IP 还通：$(cat "$WORK/body")"
[ "$(outbox_count "$SUBJECT_BLOCKED")" = "0" ] || fail "只有 1 轮被墙不应告警"
check >/dev/null; expect_code 200 "手动检测（被墙第 2 轮）"
deadline=$((SECONDS + NOTIFY_WAIT_SECONDS))
until [ "$(outbox_count "$SUBJECT_BLOCKED")" -ge 1 ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${NOTIFY_WAIT_SECONDS}s 内没有被墙告警：$(hub GET "/api/admin/mail-outbox?pageSize=$OUTBOX_PAGE_SIZE" | jq -c '[.data.items[].subject]')"
  sleep "$POLL_SECONDS"
done
sleep 5
[ "$(outbox_count "$SUBJECT_BLOCKED")" = "1" ] || fail "同一次被墙只告警一次"
[ "$(hub GET "/api/admin/servers/$SERVER_ID" | jq -r .data.reach.blocked)" = "1" ] || fail "服务器列表应标出 1 个被墙的地址：$(cat "$WORK/body")"
[ "$(hub GET /api/admin/analytics/servers | jq -c --arg h "$NODE_HOST" '[.data.reach.counts.blocked, ([.data.reach.problems[] | select(.address == $h and .verdict == "blocked" and (.servers | index("'"$SERVER_NAME"'")) != null)] | length)]')" \
  = "[1,1]" ] || fail "总览应列出被墙的地址：$(cat "$WORK/body")"
[ "$(hub GET "/api/admin/audit-logs?pageSize=$AUDIT_PAGE_SIZE" | jq '[.data.items[] | select(.action == "node.reach_blocked")] | length')" = "1" ] || fail "被墙告警应记审计"

# ---- 8. 恢复：手动检测一次 → 恢复通知 ----
: > "$WORK/blocked"
check >/dev/null; expect_code 200 "手动检测（恢复）"
[ "$(jq -c '[.data.verdict, .data.ipReachable, .data.blockedSince]' "$WORK/body")" = '["ok",null,null]' ] || fail "恢复后应为正常：$(cat "$WORK/body")"
deadline=$((SECONDS + NOTIFY_WAIT_SECONDS))
until [ "$(outbox_count "$SUBJECT_RECOVERED")" -ge 1 ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${NOTIFY_WAIT_SECONDS}s 内没有恢复通知"
  sleep "$POLL_SECONDS"
done
[ "$(hub GET /api/admin/analytics/servers | jq -c '[.data.reach.counts.blocked, (.data.reach.problems | length)]')" = "[0,0]" ] || fail "恢复后总览不应再列出：$(cat "$WORK/body")"

# ---- 9. 手动检测的频控与越界：一分钟内第 7 次 429，不存在的节点 404 ----
for i in $(seq 4 "$MANUAL_LIMIT"); do check >/dev/null; expect_code 200 "第 $i 次手动检测"; done
check >/dev/null; expect_error 429 rate_limited "一分钟内第 $((MANUAL_LIMIT + 1)) 次手动检测"
hub GET /api/admin/nodes/99999999/reach >/dev/null; expect_error 404 not_found "不存在的节点"

# ---- 10. 总览分析：取值范围、与列表一致的数字、各块的形状 ----
hub POST /api/admin/users "$(login_body_of "$END_USER" "$USER_CRED")" >/dev/null; expect_code 200 "新建用户"
users_total="$(hub GET '/api/admin/users?pageSize=1' | jq -r .data.total)"
hub GET '/api/admin/analytics/users?days=7' >/dev/null; expect_code 200 "用户分区"
[ "$(jq -c '[(.data.growth | length), .data.growth[-1].totalUsers, (.data.growth[-1].newUsers >= 1), (.data.activeDays | length), (.data.subscription.days | length)]' "$WORK/body")" \
  = "[7,$users_total,true,7,7]" ] || fail "用户分区与用户列表不一致（共 $users_total 人）：$(cat "$WORK/body")"
[ "$(jq -c '.data.status | (.active + .limited + .expired + .disabled)' "$WORK/body")" = "$users_total" ] || fail "状态分布之和应等于用户数：$(jq -c .data.status "$WORK/body")"
for q in 'users?days=8' 'users?days=abc' 'traffic?days=1' 'finance?days=365' 'servers?range=30d'; do
  hub GET "/api/admin/analytics/$q" >/dev/null; expect_error 400 invalid_argument "不接受的取值 $q"
done
hub GET /api/admin/analytics/traffic >/dev/null; expect_code 200 "流量分区"
[ "$(jq -c '[(.data.days | length), (.data.hours | length), (.data.servers | type)]' "$WORK/body")" = '[30,24,"array"]' ] || fail "流量分区的形状不对：$(cat "$WORK/body")"
hub GET '/api/admin/analytics/finance?days=90' >/dev/null; expect_code 200 "财务分区"
[ "$(jq -c '[(.data.days | length), ([.data.orderKinds[].kind] | join(",")), (.data.funnel.registered >= 1)]' "$WORK/body")" \
  = '[90,"plan,renew,traffic_pack",true]' ] || fail "财务分区的形状不对：$(cat "$WORK/body")"
hub GET '/api/admin/analytics/servers?range=7d' >/dev/null; expect_code 200 "服务器分区"
[ "$(jq -c --arg n "$SERVER_NAME" '[([.data.availability[] | select(.name == $n and .percent > 0 and .percent <= 100)] | length), (.data.online | length > 0)]' "$WORK/body")" \
  = '[1,true]' ] || fail "服务器分区应有该服务器的可用率与在线曲线：$(cat "$WORK/body")"
hub GET /api/admin/analytics/backlog >/dev/null; expect_code 200 "待处理分区"
[ "$(jq -c '[.data.tickets, .data.provisions, .data.panels, .data.outbox] | map(. != null)' "$WORK/body")" = '[true,true,true,true]' ] || fail "超管应看到待处理的全部四块：$(cat "$WORK/body")"
# 告警与恢复两封信进了发件箱（SMTP 连不上，仍是待发或已失败）
[ "$(jq -c '(.data.outbox.mailPending + .data.outbox.mailFailed24h) >= 2' "$WORK/body")" = "true" ] || fail "发件箱积压应包含这两封通知：$(jq -c .data.outbox "$WORK/body")"
for path in users traffic servers finance backlog; do
  [ "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/admin/analytics/$path")" = "401" ] || fail "未登录访问 analytics/$path 应为 401"
done

echo "IT-13 PASS"
