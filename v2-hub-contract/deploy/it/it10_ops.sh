#!/usr/bin/env bash
# IT-10 运营（docs/08，M8 DoD，docs/03 §5.20）：全新 xui-a 接入 → 设好告警邮箱、1 分钟阈值与 Telegram 会话 → 停掉面板
# → 发件箱出现“不在线”告警、Telegram 模拟端收到同一告警（只一次）→ 面板恢复 → 发件箱与模拟端都收到“已恢复在线”
# → 仪表盘的用户、服务器、充值数字与接口一致 → 落地页开着时公开套餐能看到在售套餐、关掉后为空 → 收支 CSV 能导出（BOM、表头、今天的充值）。
# 黑盒，只用 curl / jq / docker / python3（Telegram 模拟端）。SMTP 指向没有监听的端口：邮件视为已配置，断言只看发件箱。
# 注意：会重建 xui-a 容器（不挂数据卷）并停 / 启它一次；hub-server 用缩短了探测间隔的临时配置启动。
# 掉线告警的检查周期固定 1 分钟、阈值最小 1 分钟，整条用例约需 3–4 分钟。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile xui)
BASE=http://127.0.0.1:8080
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
ROOT_USER=it10-root  # 与前面用例的超管区分（run_all 只在开头重置一次库）
END_USER=it10-user
CRED="it-$(openssl rand -hex 12)"
USER_CRED="it-$(openssl rand -hex 12)"
BOT_TOKEN="it$(openssl rand -hex 8)"  # 只发给本机模拟端的假 token
SITE_NAME="IT10 站点"
SERVER_NAME=it10-xui-a
MAIL_DOMAIN=it10.example.com
ALERT_MAIL="ops@$MAIL_DOMAIN"
CHAT_ID=-1001234567890
SUBJECT_DOWN="【${SITE_NAME}】服务器 $SERVER_NAME 不在线"
SUBJECT_UP="【${SITE_NAME}】服务器 $SERVER_NAME 已恢复在线"
SMTP_HOST=127.0.0.1
SMTP_DEAD_PORT=1        # 没有服务监听，连接立即被拒
TG_PORT=18765           # Telegram 模拟端
TOPUP_AMOUNT=5000       # 分
PLAN_NAME=it10-plan
PLAN_PRICE=1500
GIB=1073741824
PROBE_INTERVAL=3s
# 探测 3 秒一次；告警检查每分钟一轮、阈值 1 分钟：不在线后最迟约 2 分钟告警，恢复后最迟约 1 分钟通知
ALERT_WAIT_SECONDS=180
RECOVER_WAIT_SECONDS=120
POLL_SECONDS=3
OUTBOX_PAGE_SIZE=100
WORK="$(mktemp -d /tmp/it10.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-10: $*"; exit 1; }

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
    -H "X-CSRF-Token: $csrf" --data '{"name":"it10","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty'
}
XUI_TOKEN="$(xui_token)"; [ -n "$XUI_TOKEN" ] || fail "xui-a 签发 token 失败"

# ---- 2. Telegram 模拟端：记下每次 sendMessage 的路径与请求体，一律回 ok ----
cat > "$WORK/tg_mock.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
LOG = sys.argv[2]
class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        with open(LOG, 'a', encoding='utf-8') as f:
            f.write(json.dumps({'path': self.path, 'body': json.loads(body or b'{}')}, ensure_ascii=False) + '\n')
        out = json.dumps({'ok': True, 'result': {}}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(out)))
        self.end_headers()
        self.wfile.write(out)
    def log_message(self, *args):
        pass
HTTPServer(('127.0.0.1', int(sys.argv[1])), Handler).serve_forever()
PY
: > "$WORK/tg.log"
set -m
python3 -I "$WORK/tg_mock.py" "$TG_PORT" "$WORK/tg.log" &
IT_PIDS+=($!)
set +m
wait_port "$TG_PORT" 10 || fail "Telegram 模拟端没起来"
# tg_count <文字>：模拟端收到的、发往本会话且正文含该文字的消息数
tg_count() { jq -s --arg t "$1" --arg c "$CHAT_ID" --arg p "/bot$BOT_TOKEN/sendMessage" \
  '[.[] | select(.path == $p and (.body.chat_id | tostring) == $c and (.body.text | contains($t)))] | length' "$WORK/tg.log"; }

# ---- 3. hub-server（探测 3 秒一次，SMTP 与 Telegram 指向本机）与超管 ----
it_hub_config "$WORK/hub-server.yaml" -e "s/^  probeInterval: .*/  probeInterval: $PROBE_INTERVAL/" || fail "派生配置失败"
grep -q "probeInterval: $PROBE_INTERVAL" "$WORK/hub-server.yaml" || fail "临时配置没有改到探测间隔"
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml" \
  HUB_SMTP_HOST="$SMTP_HOST" HUB_SMTP_PORT="$SMTP_DEAD_PORT" HUB_SMTP_FROM="noreply@$MAIL_DOMAIN" HUB_SMTP_TLS=none \
  HUB_TELEGRAM_BOT_TOKEN="$BOT_TOKEN" HUB_TELEGRAM_API_BASE="http://127.0.0.1:$TG_PORT"
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
# 超管登录：首次登录当场绑定两步验证（R10）；登录即视为刚做过二次验证，联调配置把有效期拉长（common.sh it_admin_login）
CSRF="$(it_admin_login "$HUB_JAR" "$ROOT_USER" "$CRED")" || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }
# outbox_count <主题>：发件箱里发给告警邮箱的这封信有几封
outbox_count() {
  hub GET "/api/admin/mail-outbox?pageSize=$OUTBOX_PAGE_SIZE" | jq --arg r "$ALERT_MAIL" --arg s "$1" '[.data.items[] | select(.recipient == $r and .subject == $s)] | length'
}

# ---- 4. 运营设置：告警邮箱、1 分钟阈值、Telegram 会话；测试消息能到模拟端 ----
hub PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" --arg m "$ALERT_MAIL" --arg c "$CHAT_ID" \
  '{site:{name:$n},alert:{emails:[$m],offlineMinutes:1},telegram:{chatId:$c,notifyAlerts:true}}')" >/dev/null
expect_code 200 "保存运营设置"
[ "$(jq -r '.data.telegramEnabled' "$WORK/body")" = "true" ] || fail "配了 token 后设置里应显示 Telegram 可用：$(cat "$WORK/body")"
hub POST /api/admin/settings/test-telegram >/dev/null; expect_code 200 "发 Telegram 测试消息"
[ "$(tg_count "测试消息")" = "1" ] || fail "模拟端应收到一条测试消息：$(cat "$WORK/tg.log")"

# ---- 5. 接入服务器，等它在线 ----
SERVER_ID="$(hub POST /api/admin/servers "$(jq -nc --arg n "$SERVER_NAME" --arg u "$XUI_URL" --arg t "$XUI_TOKEN" \
  '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
server_status() { hub GET "/api/admin/servers/$SERVER_ID" | jq -r .data.status; }
for _ in $(seq 1 20); do [ "$(server_status)" = "online" ] && break; sleep 1; done
[ "$(server_status)" = "online" ] || fail "服务器应在线：$(cat "$WORK/body")"

# ---- 6. 停掉面板 → 告警邮件与 Telegram 各一条 ----
"${COMPOSE[@]}" stop xui-a >/dev/null 2>&1 || fail "停 xui-a 失败"
deadline=$((SECONDS + ALERT_WAIT_SECONDS))
until [ "$(outbox_count "$SUBJECT_DOWN")" -ge 1 ] && [ "$(tg_count "$SERVER_NAME 不在线")" -ge 1 ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${ALERT_WAIT_SECONDS}s 内没有告警：状态 $(server_status)，模拟端 $(cat "$WORK/tg.log")"
  sleep "$POLL_SECONDS"
done
# 全量跑时前面用例接入的服务器也在库里：仪表盘的总数与在线数以服务器列表为准
servers_summary() { hub GET /api/admin/servers | jq -c '[(.data | length), ([.data[] | select(.status == "online")] | length)]'; }
dash_servers() { hub GET /api/admin/dashboard | jq -c '[.data.servers.total, .data.servers.online]'; }
[ "$(dash_servers)" = "$(servers_summary)" ] || fail "仪表盘服务器数与列表不一致：$(dash_servers) vs $(servers_summary)"
[ "$(hub GET /api/admin/dashboard | jq --arg n "$SERVER_NAME" '[.data.servers.offline[] | select(.name == $n and .offlineSince != null)] | length')" = "1" ] \
  || fail "仪表盘的不在线列表应有该服务器：$(cat "$WORK/body")"
# 再过一轮检查也不重复告警
sleep 65
[ "$(outbox_count "$SUBJECT_DOWN")" = "1" ] || fail "同一次不在线只告警一次（邮件）"
[ "$(tg_count "$SERVER_NAME 不在线")" = "1" ] || fail "同一次不在线只告警一次（Telegram）：$(cat "$WORK/tg.log")"

# ---- 7. 面板恢复 → 恢复通知 ----
"${COMPOSE[@]}" start xui-a >/dev/null 2>&1 || fail "启动 xui-a 失败"
deadline=$((SECONDS + RECOVER_WAIT_SECONDS))
until [ "$(outbox_count "$SUBJECT_UP")" -ge 1 ] && [ "$(tg_count "$SERVER_NAME 已恢复在线")" -ge 1 ]; do
  [ "$SECONDS" -lt "$deadline" ] || fail "${RECOVER_WAIT_SECONDS}s 内没有恢复通知：状态 $(server_status)，模拟端 $(cat "$WORK/tg.log")"
  sleep "$POLL_SECONDS"
done
[ "$(server_status)" = "online" ] || fail "恢复后服务器应在线"

# ---- 8. 充值与仪表盘数字 ----
hub PUT /api/admin/settings '{"shop":{"enabled":true}}' >/dev/null; expect_code 200 "开商店"
hub POST /api/admin/users "$(login_body_of "$END_USER" "$USER_CRED")" >/dev/null; expect_code 200 "新建用户"
code="$(hub POST /api/admin/topup-codes "$(jq -nc --argjson a "$TOPUP_AMOUNT" '{amount:$a,count:1,batch:"it10"}')" | jq -r '.data.codes[0] // empty')"
expect_code 200 "生成充值码"
[ -n "$code" ] || fail "应带回充值码：$(cat "$WORK/body")"
USER_JAR="$WORK/user.jar"
USER_CSRF="$(call "$USER_JAR" "" POST /api/user/auth/login "$(login_body_of "$END_USER" "$USER_CRED")" | jq -r '.data.csrfToken // empty')"
[ -n "$USER_CSRF" ] || fail "用户登录失败：$(cat "$WORK/body")"
call "$USER_JAR" "$USER_CSRF" POST /api/user/wallet/redeem "$(jq -nc --arg c "$code" '{code:$c}')" >/dev/null; expect_code 200 "兑换充值码"
users_total="$(hub GET '/api/admin/users?pageSize=1' | jq -r .data.total)"
# 全量跑时前面用例今天也充过值：今天的充值以收支统计接口为准，且至少包含这一笔
today_topup="$(hub GET '/api/admin/finance/stats?granularity=day&days=1' | jq -r '.data.days[-1].topup')"
[ "$today_topup" -ge "$TOPUP_AMOUNT" ] || fail "收支统计里今天的充值应包含这一笔：$(cat "$WORK/body")"
dash="$(hub GET /api/admin/dashboard)"; expect_code 200 "取仪表盘"
[ "$(printf '%s' "$dash" | jq -c --arg n "$SERVER_NAME" '[.data.users.total, .data.finance.todayTopup, (.data.traffic.days | length), ([.data.servers.offline[] | select(.name == $n)] | length)]')" \
  = "[$users_total,$today_topup,7,0]" ] || fail "仪表盘数字与库里不一致（用户 ${users_total}）：$dash"
[ "$(dash_servers)" = "$(servers_summary)" ] || fail "恢复后仪表盘服务器数与列表不一致：$(dash_servers) vs $(servers_summary)"

# ---- 9. 落地页与公开套餐 ----
hub POST /api/admin/plans "$(jq -nc --arg n "$PLAN_NAME" --argjson l "$GIB" --argjson p "$PLAN_PRICE" \
  '{name:$n,trafficLimit:$l,durationDays:30,nodeGroupIds:[],isOnSale:true,description:"卖点一",prices:[{durationDays:30,price:$p}]}')" >/dev/null
expect_code 200 "新建在售套餐"
hub PUT /api/admin/settings '{"landing":{"enabled":true,"headline":"IT10 标题"}}' >/dev/null; expect_code 200 "开落地页"
public_plans() { curl -s "$BASE/api/user/site/plans" | jq -c --arg n "$PLAN_NAME" '[.data[] | select(.name == $n) | [.trafficLimit, .prices[0].price, (has("canBuy") or has("isCurrent"))]]'; }
for _ in $(seq 1 15); do [ "$(public_plans)" != "[]" ] && break; sleep 2; done  # 设置有 30 秒缓存
[ "$(public_plans)" = "[[$GIB,$PLAN_PRICE,false]]" ] || fail "落地页开着时公开套餐应有在售套餐且不含个人字段：$(public_plans)"
[ "$(curl -s "$BASE/api/user/site" | jq -r .data.landing.headline)" = "IT10 标题" ] || fail "站点信息应带落地页标题"
hub PUT /api/admin/settings '{"landing":{"enabled":false}}' >/dev/null; expect_code 200 "关落地页"
for _ in $(seq 1 15); do [ "$(public_plans)" = "[]" ] && break; sleep 2; done
[ "$(public_plans)" = "[]" ] || fail "落地页关掉后公开套餐应为空"
[ "$(curl -s "$BASE/api/user/site" | jq -r .data.landing.intro)" = "" ] || fail "落地页关掉后不应带出介绍"

# ---- 10. 收支 CSV 导出 ----
csv_code="$(curl -s -o "$WORK/finance.csv" -D "$WORK/headers" -w '%{http_code}' -b "$HUB_JAR" "$BASE/api/admin/finance/stats.csv?granularity=day&days=1")"
[ "$csv_code" = "200" ] || fail "导出 CSV：HTTP $csv_code $(cat "$WORK/finance.csv")"
grep -qi '^content-type: text/csv' "$WORK/headers" || fail "CSV 的 Content-Type 不对：$(cat "$WORK/headers")"
grep -qi '^content-disposition: attachment; filename=finance-day-' "$WORK/headers" || fail "CSV 应作为附件下载：$(cat "$WORK/headers")"
[ "$(head -c 3 "$WORK/finance.csv" | od -An -tx1 | tr -d ' \n')" = "efbbbf" ] || fail "CSV 应以 UTF-8 BOM 开头"
head -1 "$WORK/finance.csv" | grep -q '日期,充值入账（元）' || fail "CSV 表头不对：$(head -1 "$WORK/finance.csv")"
today_yuan="$(printf '%d.%02d' $((today_topup / 100)) $((today_topup % 100)))"
grep -q "^$(date +%F),$today_yuan," "$WORK/finance.csv" || fail "CSV 应有今天的充值 $today_yuan 元：$(cat "$WORK/finance.csv")"
curl -s -o /dev/null -w '%{http_code}' "$BASE/api/admin/finance/stats.csv?granularity=day&days=1" | grep -q '^401$' || fail "未登录导出 CSV 应 401"

echo "IT-10 PASS"
