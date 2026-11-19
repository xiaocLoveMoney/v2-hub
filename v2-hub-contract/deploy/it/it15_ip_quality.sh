#!/usr/bin/env bash
# IT-15 IP 质量检测（docs/08，docs/03 §5.23，Ruling-81、Ruling-82）：不连任何真实第三方——
# 平台侧的数据源（HTTP）与 DNS（黑名单、Cymru、Tor）指向宿主机上的 python 模拟端；服务器侧的探测目标与邮局
# 指向 sshbox 容器里的 python 模拟端（容器里的 curl 与探测脚本都是真的）。
# → 手动接入 sshbox 的面板为服务器，发起检测：只做平台侧，服务器侧 no_host
# → 系统设置的 ipQuality 分组：数据源目录只读、配了 key 的显示已配置，非法间隔 400
# → 给服务器绑定 SSH 后检测：服务器侧完成，出口 IP、各源、黑名单、流媒体、25 端口都与模拟端一致；进行中再发起 409
# → 改模拟端的数据再测一次：历史多一条，两条详情有差异（Netflix 变成仅自制、黑名单多一个上榜）
# → 只有 server:* 的管理员发起 → 只做平台侧（no_permission）；只有 host:read 的管理员看不到记录（403）
# → 同一管理员超出频控 429；全部响应与 hub-server 日志里不出现模拟的数据源 key。
# 黑盒，只用 curl / jq / docker / ssh-keygen / python3。会构建并重建 sshbox 容器。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile sshbox)
BASE=http://127.0.0.1:8080
SSH_PORT="${SSHBOX_SSH_PORT:-12222}"
PANEL_PORT="${SSHBOX_PANEL_PORT:-12056}"
HOST_ADDR=127.0.0.1
# 绑定 SSH 用 localhost：IT-14 留下的主机已占用“127.0.0.1 + 端口”（run_all 只在开头重置一次库）
SSH_HOST=localhost
PANEL_HTTP="http://$HOST_ADDR:$PANEL_PORT"
XUI_BIN=/usr/local/x-ui/x-ui
ROOT_USER=it15-root
LIMITED_USER=it15-server-admin
VIEWER_USER=it15-host-viewer
CRED="it-$(openssl rand -hex 12)"
SEED_TOKEN_NAME=it15-seed
SERVER_NAME=it15-node
SOURCE_PORT="${IT15_SOURCE_PORT:-18915}"
DNS_PORT="${IT15_DNS_PORT:-18953}"
BOX_HTTP_PORT=18080   # 容器里的模拟端，只在容器内可达
BOX_SMTP_PORT=2525
IPQS_KEY="it15-ipqs-$(openssl rand -hex 8)"
EGRESS_IP=9.9.9.9
MANUAL_LIMIT=4
POLL_INTERVAL=1s
CHECK_WAIT_SECONDS=120
BOX_WAIT_SECONDS=60
POLL_SECONDS=1
WORK="$(mktemp -d /tmp/it15.XXXXXX)"
trap 'rm -rf "$WORK"; "${COMPOSE[@]}" rm -sf sshbox >/dev/null 2>&1; stop_all' EXIT

fail() { echo "IT-15: $*"; exit 1; }

# ---- 1. 宿主机上的模拟端：数据源（HTTP）与 DNS；variant 文件切换两套数据 ----
echo 1 > "$WORK/variant"
cat > "$WORK/source_mock.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs
PORT, VARIANT, IPQS_KEY = int(sys.argv[1]), sys.argv[2], sys.argv[3]
def variant():
    with open(VARIANT, encoding='utf-8') as f:
        return f.read().strip()
class Handler(BaseHTTPRequestHandler):
    def reply(self, status, payload):
        out = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(out)))
        self.end_headers()
        self.wfile.write(out)
    def do_GET(self):
        u = urlparse(self.path)
        q = parse_qs(u.query)
        if u.path == '/ip2location/':
            self.reply(200, {'ip': q.get('ip', [''])[0], 'country_code': 'DE', 'region_name': 'Hessen', 'city_name': 'Frankfurt',
                             'latitude': 50.11, 'longitude': 8.68, 'time_zone': '+01:00', 'asn': '64500', 'as': 'Example Hosting', 'is_proxy': False})
        elif u.path == '/ipqs/api/json/ip/%s/%s' % (IPQS_KEY, '9.9.9.9'):
            score = 50 if variant() == '1' else 88
            self.reply(200, {'success': True, 'message': 'Success', 'fraud_score': score, 'country_code': 'DE', 'proxy': False, 'vpn': False,
                             'tor': False, 'active_vpn': False, 'active_tor': False, 'recent_abuse': False, 'bot_status': False, 'connection_type': 'Data Center'})
        else:
            self.reply(404, {'error': 'not found'})
    def log_message(self, *args):
        pass
HTTPServer(('127.0.0.1', PORT), Handler).serve_forever()
PY
cat > "$WORK/dns_mock.py" <<'PY'
import socket, struct, sys
PORT, VARIANT = int(sys.argv[1]), sys.argv[2]
ZONES = ['zen.spamhaus.org', 'b.barracudacentral.org', 'bl.spamcop.net', 'psbl.surriel.com', 'bl.mailspike.net', 'dnsbl-1.uceprotect.net',
         'all.s5h.net', 'spam.dnsbl.anonmails.de', 'bl.blocklist.de', 'dnsbl.spfbl.net', 'truncate.gbudb.net', 'dnsbl.justspam.org',
         'bl.0spam.org', 'rbl.interserver.net', 'dnsbl.kempt.net', 'dnsbl.dronebl.org', 'bl.suomispam.net']
TYPE_A, TYPE_TXT, CLASS_IN = 1, 16, 1
def variant():
    with open(VARIANT, encoding='utf-8') as f:
        return f.read().strip()
def records(name):
    """返回 (类型, 数据) 列表；None 表示 NXDOMAIN。"""
    a = {}
    for z in ZONES:
        a['2.0.0.127.' + z] = '127.0.0.2'          # RFC 5782 自检
    if variant() != '1':
        a['9.9.9.9.bl.spamcop.net'] = '127.0.0.2'  # 第二套数据：多一个上榜
    txt = {'9.9.9.9.origin.asn.cymru.com': '64500 | 9.9.9.0/24 | US | arin | 2020-01-01',
           'as64500.asn.cymru.com': '64500 | US | arin | 2001-01-01 | EXAMPLE - Example Hosting, US'}
    if name in a:
        return [(TYPE_A, socket.inet_aton(a[name]))]
    if name in txt:
        raw = txt[name].encode()
        return [(TYPE_TXT, bytes([len(raw)]) + raw)]
    return None
def answer(packet):
    qid, flags, qd = struct.unpack('>HHH', packet[:6])
    pos, labels = 12, []
    while packet[pos] != 0:
        n = packet[pos]
        labels.append(packet[pos + 1:pos + 1 + n].decode().lower())
        pos += n + 1
    pos += 1
    qtype, qclass = struct.unpack('>HH', packet[pos:pos + 4])
    question = packet[12:pos + 4]
    name = '.'.join(labels)
    recs = records(name)
    rcode = 3 if recs is None else 0
    body = b''
    count = 0
    for rtype, data in recs or []:
        if rtype != qtype:
            continue  # 同名的其它类型：NOERROR 无记录
        body += b'\xc0\x0c' + struct.pack('>HHIH', rtype, CLASS_IN, 60, len(data)) + data
        count += 1
    header = struct.pack('>HHHHHH', qid, 0x8180 | rcode, 1, count, 0, 0)
    return header + question + body
sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.bind(('127.0.0.1', PORT))
while True:
    data, addr = sock.recvfrom(4096)
    try:
        sock.sendto(answer(data), addr)
    except Exception:
        pass  # 解析不了的包不回
PY
set -m
python3 -I "$WORK/source_mock.py" "$SOURCE_PORT" "$WORK/variant" "$IPQS_KEY" &
IT_PIDS+=($!)
python3 -I "$WORK/dns_mock.py" "$DNS_PORT" "$WORK/variant" &
IT_PIDS+=($!)
set +m
wait_port "$SOURCE_PORT" 10 || fail "数据源模拟端没起来"

# ---- 2. 全新的 sshbox，容器里起服务器侧的模拟端 ----
ssh-keygen -q -t ed25519 -N '' -C it15 -f "$WORK/id" || fail "生成密钥对失败"
export IT_SSHBOX_PUBKEY
IT_SSHBOX_PUBKEY="$(cat "$WORK/id.pub")"
"${COMPOSE[@]}" build -q sshbox >/dev/null 2>"$WORK/build.log" || fail "构建 sshbox 失败：$(tail -5 "$WORK/build.log")"
box_exec() { "${COMPOSE[@]}" exec -T sshbox "$@"; }
"${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes sshbox >/dev/null 2>&1 || fail "sshbox 没起来"
wait_port "$SSH_PORT" "$BOX_WAIT_SECONDS" || fail "sshbox 的 sshd 没就绪"
deadline=$((SECONDS + BOX_WAIT_SECONDS))
until curl -s -o /dev/null "$PANEL_HTTP/"; do
  [ "$SECONDS" -lt "$deadline" ] || fail "sshbox 的面板没就绪"
  sleep "$POLL_SECONDS"
done
cat > "$WORK/probe_mock.py" <<'PY'
import socketserver, sys, threading
from http.server import BaseHTTPRequestHandler, HTTPServer
HTTP_PORT, SMTP_PORT, VARIANT = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
def variant():
    with open(VARIANT, encoding='utf-8') as f:
        return f.read().strip()
def pages():
    licensed = 'licensed ok' if variant() == '1' else 'Oh no! not available'
    return {'/eg': 'fl=1\nip=9.9.9.9\nloc=DE\n', '/egb': '9.9.9.9\n',
            '/nfo': '<html>{"requestCountry":{"id":"DE","x":1}}</html>', '/nfl': '<html>%s</html>' % licensed,
            '/yt': '<html>"INNERTUBE_CONTEXT_GL":"DE" ad-free</html>', '/pv': '{"currentTerritory":"DE"}',
            '/tt': '{"region":"DE"}', '/cgw': '{"cookie_requirements":"ok"}', '/cga': '<html>ok</html>', '/cgt': 'ip=9.9.9.9\nloc=DE\n'}
class Web(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/rdt':
            self.send_response(403); self.end_headers(); return
        body = pages().get(self.path)
        if body is None:
            self.send_response(404); self.end_headers(); return
        out = body.encode()
        self.send_response(200)
        self.send_header('Content-Length', str(len(out)))
        self.end_headers()
        self.wfile.write(out)
    def log_message(self, *args):
        pass
class Smtp(socketserver.StreamRequestHandler):
    def handle(self):
        self.wfile.write(b'220 it15 ESMTP\r\n')
        line = self.rfile.readline()
        if line.upper().startswith(b'QUIT'):
            self.wfile.write(b'221 bye\r\n')
class ThreadedTCP(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True
threading.Thread(target=ThreadedTCP(('127.0.0.1', SMTP_PORT), Smtp).serve_forever, daemon=True).start()
HTTPServer(('127.0.0.1', HTTP_PORT), Web).serve_forever()
PY
box_exec sh -c 'cat > /root/probe_mock.py' < "$WORK/probe_mock.py" || fail "传模拟端脚本失败"
box_exec sh -c 'echo 1 > /root/variant && (nohup python3 -I /root/probe_mock.py '"$BOX_HTTP_PORT $BOX_SMTP_PORT"' /root/variant >/root/probe_mock.log 2>&1 &)' || fail "容器里起模拟端失败"
deadline=$((SECONDS + BOX_WAIT_SECONDS))
until box_exec curl -s -o /dev/null "http://127.0.0.1:$BOX_HTTP_PORT/eg"; do
  [ "$SECONDS" -lt "$deadline" ] || fail "容器里的模拟端没就绪：$(box_exec cat /root/probe_mock.log)"
  sleep "$POLL_SECONDS"
done
set_variant() { echo "$1" > "$WORK/variant"; box_exec sh -c "echo $1 > /root/variant"; }
SEED_TOKEN="$(box_exec "$XUI_BIN" setting -tokenName "$SEED_TOKEN_NAME" -tokenScope admin -getApiToken | sed -n 's/^apiToken: //p' | tr -d '\r')"
[ -n "$SEED_TOKEN" ] || fail "面板签发令牌失败"

# ---- 3. hub-server：允许回环、1 秒领取、检测目标全部指向模拟端、手动频控 4 次 ----
sed -e 's/^  allowLoopback: .*/  allowLoopback: true/' -e "s/^  pollInterval: .*/  pollInterval: $POLL_INTERVAL/" \
  -e "s/^  manualLimit: .*/  manualLimit: $MANUAL_LIMIT/" -e "s/^  dnsResolver: .*/  dnsResolver: \"127.0.0.1:$DNS_PORT\"/" \
  -e "s|^    sourceBaseUrl: .*|    sourceBaseUrl: \"http://127.0.0.1:$SOURCE_PORT\"|" \
  -e "s|^    probeBaseUrl: .*|    probeBaseUrl: \"http://127.0.0.1:$BOX_HTTP_PORT\"|" \
  -e "s|^    probeMailAddr: .*|    probeMailAddr: \"127.0.0.1:$BOX_SMTP_PORT\"|" \
  "$REPO_ROOT/v2-hub-server/etc/hub-server.yaml" > "$WORK/hub-server.yaml"
for want in 'allowLoopback: true' "manualLimit: $MANUAL_LIMIT" "dnsResolver: \"127.0.0.1:$DNS_PORT\"" "sourceBaseUrl: \"http://127.0.0.1:$SOURCE_PORT\"" \
  "probeBaseUrl: \"http://127.0.0.1:$BOX_HTTP_PORT\"" "probeMailAddr: \"127.0.0.1:$BOX_SMTP_PORT\""; do
  grep -qF "$want" "$WORK/hub-server.yaml" || fail "临时配置没有改到：$want"
done
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml" HUB_IPQ_IPQS_KEY="$IPQS_KEY" HUB_IPQ_IPAPIIS_KEY="" HUB_IPQ_ABUSEIPDB_KEY="" HUB_IPQ_IP2LOCATION_KEY=""
grep -q 'testOverrides is set' /tmp/it-v2-hub-server.log || fail "联调覆盖开着时启动应打警告"
HUB_JAR="$WORK/hub.jar"
: > "$WORK/responses.log"
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
call() {
  local args=(-s -o "$WORK/body" -w '%{http_code}' -b "$1" -c "$1" -X "$3" -H 'Content-Type: application/json')
  [ -n "$2" ] && args+=(-H "X-CSRF-Token: $2")
  [ -n "${5:-}" ] && args+=(--data "$5")
  curl "${args[@]}" "$BASE$4" > "$WORK/code"
  cat "$WORK/body" >> "$WORK/responses.log"; echo >> "$WORK/responses.log"
  cat "$WORK/body"
}
expect_code() { [ "$(cat "$WORK/code")" = "$1" ] || fail "$2：期望 HTTP $1，实际 $(cat "$WORK/code") $(cat "$WORK/body")"; }
expect_error() { expect_code "$1" "$3"; [ "$(jq -r .code "$WORK/body")" = "$2" ] || fail "$3：期望错误码 $2，实际 $(cat "$WORK/body")"; }
login() { call "$1" "" POST /api/admin/auth/login "$(login_body_of "$2" "$CRED")" | jq -r '.data.csrfToken // empty'; }
CSRF="$(login "$HUB_JAR" "$ROOT_USER")"
[ -n "$CSRF" ] || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }
# wait_check <检测 id>：等检测结束，详情留在 $WORK/check.json，打印最终状态
wait_check() {
  local deadline=$((SECONDS + CHECK_WAIT_SECONDS)) status
  while :; do
    hub GET "/api/admin/ip-checks/$1" > "$WORK/check.json"
    status="$(jq -r '.data.status // empty' "$WORK/check.json")"
    case "$status" in queued|running) ;; *) printf '%s' "$status"; return 0 ;; esac
    [ "$SECONDS" -lt "$deadline" ] || { printf 'timeout'; return 0; }
    sleep "$POLL_SECONDS"
  done
}
# start_check <cookie jar> <csrf>：发起一次检测，打印 id
start_check() { call "$1" "$2" POST "/api/admin/servers/$SERVER_ID/ip-checks" | jq -r '.data.id // empty'; }
field() { jq -r "$1" "$WORK/check.json"; }

# ---- 4. 手动接入面板（没绑 SSH）：只做平台侧 ----
SERVER_ID="$(hub POST /api/admin/servers "$(jq -nc --arg n "$SERVER_NAME" --arg u "$PANEL_HTTP" --arg t "$SEED_TOKEN" \
  '{name:$n,providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:"verify"}')" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
hub GET "/api/admin/servers/$SERVER_ID/ip-checks" >/dev/null; expect_code 200 "空的检测历史"
[ "$(jq -c '.data' "$WORK/body")" = '{"items":[],"total":0}' ] || fail "没检测过时历史应为空：$(cat "$WORK/body")"
ID1="$(start_check "$HUB_JAR" "$CSRF")"; expect_code 200 "发起检测（没绑 SSH）"
[ "$(jq -r '.data.status' "$WORK/body")" = "queued" ] || fail "新发起的检测应在排队：$(cat "$WORK/body")"
STATUS="$(wait_check "$ID1")"
[ "$(field .data.hostProbe)" = "no_host" ] || fail "没绑 SSH 时服务器侧应为 no_host：$(jq -c .data "$WORK/check.json")"
# 面板地址是回环：面板报了公网 IP 就用它做平台侧，没报就失败 no_public_ip（容器有没有外网而定）
case "$STATUS" in
  succeeded) [ "$(field .data.ipSource)" = "panel" ] && [ "$(field '.data.result.stacks.v4.media.status')" = "no_host" ] \
    || fail "没绑 SSH 时 IP 应取面板报告、流媒体段标 no_host：$(jq -c .data "$WORK/check.json")" ;;
  failed) [ "$(field .data.errorCode)" = "no_public_ip" ] || fail "失败原因应是 no_public_ip：$(jq -c .data "$WORK/check.json")" ;;
  *) fail "检测没结束：$STATUS" ;;
esac

# ---- 5. 系统设置的 ipQuality 分组 ----
hub GET /api/admin/settings >/dev/null; expect_code 200 "读设置"
[ "$(jq -c '[.data.ipQuality.autoEnabled, .data.ipQuality.intervalDays]' "$WORK/body")" = "[false,7]" ] || fail "定期复检默认应关、间隔 7 天：$(jq -c .data.ipQuality "$WORK/body")"
[ "$(jq -c '[.data.ipQuality.sources[] | select(.needsKey and .isConfigured) | .key]' "$WORK/body")" = '["ipqs"]' ] \
  || fail "只有 IPQS 配了 key：$(jq -c .data.ipQuality.sources "$WORK/body")"
hub PUT /api/admin/settings '{"ipQuality":{"intervalDays":0}}' >/dev/null; expect_error 400 invalid_argument "复检间隔 0 天"
hub PUT /api/admin/settings '{"ipQuality":{"sources":[]}}' >/dev/null; expect_error 400 invalid_argument "数据源目录只读"
hub PUT /api/admin/settings '{"ipQuality":{"intervalDays":14}}' >/dev/null; expect_code 200 "改复检间隔"
[ "$(jq -r .data.ipQuality.intervalDays "$WORK/body")" = "14" ] || fail "复检间隔应已改为 14"

# ---- 6. 绑定 SSH 后完整检测 ----
CRED_ID="$(hub POST /api/admin/ssh-credentials "$(jq -nc --rawfile k "$WORK/id" '{name:"it15-key",username:"root",authType:"key",secret:$k}')" | jq -r '.data.id // empty')"
expect_code 200 "新建凭据"
hub PUT "/api/admin/servers/$SERVER_ID/host" "$(jq -nc --arg a "$SSH_HOST" --argjson p "$SSH_PORT" --argjson c "$CRED_ID" '{address:$a,sshPort:$p,credentialId:$c}')" > "$WORK/bind.json"
expect_code 200 "绑定 SSH"
VERIFY_ID="$(jq -r '.data.lastTask.id' "$WORK/bind.json")"
deadline=$((SECONDS + CHECK_WAIT_SECONDS))
while :; do
  vstatus="$(hub GET "/api/admin/host-tasks/$VERIFY_ID" | jq -r '.data.status')"
  case "$vstatus" in queued|running) ;; *) break ;; esac
  [ "$SECONDS" -lt "$deadline" ] || fail "校验连接超时"
  sleep "$POLL_SECONDS"
done
[ "$vstatus" = "succeeded" ] || fail "校验连接应成功：$(hub GET "/api/admin/host-tasks/$VERIFY_ID" | jq -c .data)"

ID2="$(start_check "$HUB_JAR" "$CSRF")"; expect_code 200 "发起完整检测"
start_check "$HUB_JAR" "$CSRF" >/dev/null; expect_error 409 conflict "进行中再发起"
[ "$(wait_check "$ID2")" = "succeeded" ] || fail "完整检测应成功：$(jq -c .data "$WORK/check.json")"
cp "$WORK/check.json" "$WORK/check2.json"
SUMMARY='[.data.scope, .data.hostProbe, .data.ip, .data.ipSource, .data.country, .data.regCountry, .data.ipKind, .data.asn, .data.usageType, .data.riskLevel, .data.dnsblListed, .data.dnsblTotal, .data.mediaOk, .data.mediaTotal, .data.port25]'
[ "$(jq -c "$SUMMARY" "$WORK/check2.json")" = "[\"full\",\"done\",\"$EGRESS_IP\",\"host\",\"DE\",\"US\",\"broadcast\",64500,\"hosting\",\"low\",0,17,5,6,\"open\"]" ] \
  || fail "完整检测的摘要不对：$(jq -c "$SUMMARY" "$WORK/check2.json")"
R='.data.result.stacks.v4'
[ "$(jq -c "[$R.sources[] | {(.key): .status}] | add" "$WORK/check2.json")" = \
  '{"abuseipdb":"not_configured","cymru":"ok","ip2location":"ok","ipapiis":"not_configured","ipqs":"ok","tor":"ok"}' ] \
  || fail "各源状态不对：$(jq -c "[$R.sources[] | {key, status}]" "$WORK/check2.json")"
[ "$(jq -c "$R.basics | [.asOrg, .prefix, .registry]" "$WORK/check2.json")" = '["EXAMPLE - Example Hosting, US","9.9.9.0/24","arin"]' ] \
  || fail "Cymru 的网络信息不对：$(jq -c "$R.basics" "$WORK/check2.json")"
[ "$(jq -c "[$R.media.items[] | {(.key): .status}] | add" "$WORK/check2.json")" = \
  '{"chatgpt":"yes","netflix":"yes","prime_video":"yes","reddit":"no","tiktok":"yes","youtube_premium":"yes"}' ] \
  || fail "流媒体结论不对：$(jq -c "[$R.media.items[] | {key, status, region, method, evidence}]" "$WORK/check2.json")"
# 模拟端在容器的回环地址上：按“连到了内网地址”判为 DNS 解锁
[ "$(jq -c "[$R.media.items[] | select(.status == \"yes\") | .method] | unique" "$WORK/check2.json")" = '["dns"]' ] \
  || fail "解锁方式应为 dns：$(jq -c "[$R.media.items[] | {key, method}]" "$WORK/check2.json")"
[ "$(jq -c "[$R.mail.items[] | .result] | unique" "$WORK/check2.json")" = '["ok"]' ] || fail "邮局 25 端口应都通：$(jq -c "$R.mail" "$WORK/check2.json")"
[ "$(jq -c "[$R.egress.ip, $R.egress.country, $R.dnsbl.unavailable]" "$WORK/check2.json")" = "[\"$EGRESS_IP\",\"DE\",0]" ] \
  || fail "出口或黑名单可用数不对：$(jq -c "[$R.egress, $R.dnsbl]" "$WORK/check2.json")"

# ---- 7. 改模拟端的数据再测：两条详情有差异 ----
set_variant 2
ID3="$(start_check "$HUB_JAR" "$CSRF")"; expect_code 200 "再发起一次"
[ "$(wait_check "$ID3")" = "succeeded" ] || fail "第二次完整检测应成功：$(jq -c .data "$WORK/check.json")"
[ "$(jq -c "[.data.riskLevel, .data.dnsblListed, ($R.media.items[] | select(.key == \"netflix\") | .status)]" "$WORK/check.json")" = '["high",1,"originals_only"]' ] \
  || fail "第二次检测应看到风险升高、多一个上榜、Netflix 仅自制：$(jq -c "[.data.riskLevel, .data.dnsblListed, [$R.media.items[] | {key, status}]]" "$WORK/check.json")"
! diff -q <(jq -S "$R" "$WORK/check2.json") <(jq -S "$R" "$WORK/check.json") >/dev/null || fail "两次报告应有差异"
hub GET "/api/admin/servers/$SERVER_ID/ip-checks?pageSize=2" >/dev/null; expect_code 200 "历史分页"
[ "$(jq -c '[.data.total, [.data.items[].id]]' "$WORK/body")" = "[3,[$ID3,$ID2]]" ] || fail "历史应有 3 条、新的在前：$(jq -c '.data | {total, ids: [.items[].id]}' "$WORK/body")"
jq -e '.data.items[0] | has("result") | not' "$WORK/body" >/dev/null || fail "历史列表不应带完整报告"

# ---- 8. 权限：只有 server:* 降级为平台侧；只有 host:read 看不到 ----
hub POST /api/admin/roles '{"code":"it15_server_admin","name":"IT15 服务器管理","permissions":["server:read","server:write"]}' >/dev/null; expect_code 200 "建角色"
hub POST /api/admin/admins "$(jq -nc --arg u "$LIMITED_USER" --arg p "$CRED" '{username:$u,password:$p,roles:["it15_server_admin"]}')" >/dev/null; expect_code 200 "建管理员"
hub POST /api/admin/roles '{"code":"it15_host_viewer","name":"IT15 主机只读","permissions":["host:read"]}' >/dev/null; expect_code 200 "建角色"
hub POST /api/admin/admins "$(jq -nc --arg u "$VIEWER_USER" --arg p "$CRED" '{username:$u,password:$p,roles:["it15_host_viewer"]}')" >/dev/null; expect_code 200 "建管理员"
LJAR="$WORK/limited.jar"; LCSRF="$(login "$LJAR" "$LIMITED_USER")"
ID4="$(start_check "$LJAR" "$LCSRF")"; expect_code 200 "只有 server:* 发起"
[ "$(jq -c '[.data.scope, .data.hostProbe]' "$WORK/body")" = '["platform","no_permission"]' ] || fail "没有 host:write 应只做平台侧：$(cat "$WORK/body")"
[ "$(wait_check "$ID4")" = "succeeded" ] || fail "平台侧检测应成功：$(jq -c .data "$WORK/check.json")"
[ "$(field .data.hostProbe)" = "no_permission" ] && [ "$(field .data.mediaTotal)" = "0" ] || fail "平台侧检测不应有流媒体结果：$(jq -c .data "$WORK/check.json")"
VJAR="$WORK/viewer.jar"; VCSRF="$(login "$VJAR" "$VIEWER_USER")"
call "$VJAR" "$VCSRF" GET "/api/admin/servers/$SERVER_ID/ip-checks" >/dev/null; expect_error 403 permission_denied "只有 host:read 看历史"
call "$VJAR" "$VCSRF" GET "/api/admin/ip-checks/$ID2" >/dev/null; expect_error 403 permission_denied "只有 host:read 看报告"
call "$VJAR" "$VCSRF" POST "/api/admin/servers/$SERVER_ID/ip-checks" >/dev/null; expect_error 403 permission_denied "只有 host:read 发起"

# ---- 9. 频控：同一管理员 10 分钟内最多 4 次（这里已发起 3 次）----
ID5="$(start_check "$HUB_JAR" "$CSRF")"; expect_code 200 "第 4 次发起"
[ "$(wait_check "$ID5")" = "succeeded" ] || fail "第 4 次检测应成功"
start_check "$HUB_JAR" "$CSRF" >/dev/null; expect_error 429 rate_limited "超出频控"
hub GET /api/admin/ip-checks/999999 >/dev/null; expect_error 404 not_found "不存在的检测"

# ---- 10. 审计与泄露检查 ----
hub GET "/api/admin/audit-logs?pageSize=100" > "$WORK/audit.json"
[ "$(jq '[.data.items[] | select(.action == "ip_check.create")] | length' "$WORK/audit.json")" = "5" ] || fail "审计里应有 5 条 ip_check.create（定期复检之外的每次手动发起）"
! grep -qF -- "$IPQS_KEY" "$WORK/responses.log" || fail "响应里出现了数据源 key"
! grep -qF -- "$IPQS_KEY" /tmp/it-v2-hub-server.log || fail "hub-server 日志里出现了数据源 key"

echo "IT-15 PASS"
