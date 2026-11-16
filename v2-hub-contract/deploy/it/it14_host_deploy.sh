#!/usr/bin/env bash
# IT-14 SSH 托管与接管（docs/08，docs/03 §5.22，Ruling-68 – Ruling-71）：带 sshd 的 3x-ui 容器当“托管主机”，面板上先有一个令牌和一个入站
# → 建私钥凭据（响应不带私钥）→ 连接测试返回主机密钥指纹并检测到已有面板 → 建主机，部署任务走接管：
#   面板上多出 v2-hub 令牌而原有令牌照常可用，服务器在线、原有入站已同步；已接入后再部署 409
# → 查看状态、重启面板、重签令牌各一次；同一主机同时只有一条未结束的任务（第二个动作 409）
# → 配置证书：服务器变成 https + pin 并保持在线 → 重置面板登录口令，新口令能登录面板、审计不带口令
# → 重建容器（机器重装，主机密钥变了）：任务失败 host_key_changed，重置主机密钥后恢复
# → 任务日志与全部响应里不出现私钥、令牌、面板口令；只有 server 权限的管理员碰不到主机接口。
# 黑盒，只用 curl / jq / docker / ssh-keygen。容器里没有 systemd，systemctl 是垫片；全新安装与升级不在本用例范围（需真机）。
# 注意：会构建并重建 sshbox 容器；hub-server 用允许回环地址、缩短任务轮询间隔的临时配置启动。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
COMPOSE=(docker compose -f "$DEPLOY_DIR/docker-compose.yml" --profile sshbox)
BASE=http://127.0.0.1:8080
SSH_PORT="${SSHBOX_SSH_PORT:-12222}"
PANEL_PORT="${SSHBOX_PANEL_PORT:-12056}"
HOST_ADDR=127.0.0.1
PANEL_HTTP="http://$HOST_ADDR:$PANEL_PORT"
PANEL_HTTPS="https://$HOST_ADDR:$PANEL_PORT"
XUI_BIN=/usr/local/x-ui/x-ui
ROOT_USER=it14-root   # 与前面用例的超管区分（run_all 只在开头重置一次库）
LIMITED_USER=it14-server-admin
CRED="it-$(openssl rand -hex 12)"
CRED_NAME=it14-key
HOST_NAME=it14-node
SEED_TOKEN_NAME=it14-seed
PLATFORM_TOKEN_NAME=v2-hub
INBOUND_PORT=24014
INBOUND_REMARK=it14-existing
POLL_INTERVAL=1s
TASK_WAIT_SECONDS=120
BOX_WAIT_SECONDS=60
POLL_SECONDS=1
AUDIT_PAGE_SIZE=100
WORK="$(mktemp -d /tmp/it14.XXXXXX)"
trap 'rm -rf "$WORK"; "${COMPOSE[@]}" rm -sf sshbox >/dev/null 2>&1; stop_all' EXIT

fail() { echo "IT-14: $*"; exit 1; }

# ---- 1. 密钥对与全新的托管主机 ----
ssh-keygen -q -t ed25519 -N '' -C it14 -f "$WORK/id" || fail "生成密钥对失败"
export IT_SSHBOX_PUBKEY
IT_SSHBOX_PUBKEY="$(cat "$WORK/id.pub")"
"${COMPOSE[@]}" build -q sshbox >/dev/null 2>"$WORK/build.log" || fail "构建 sshbox 失败：$(tail -5 "$WORK/build.log")"
box_exec() { "${COMPOSE[@]}" exec -T sshbox "$@"; }
# box_up：重建容器（全新面板、新的主机密钥）并等 sshd 与面板就绪
box_up() {
  "${COMPOSE[@]}" up -d --force-recreate --renew-anon-volumes sshbox >/dev/null 2>&1 || return 1
  wait_port "$SSH_PORT" "$BOX_WAIT_SECONDS" || return 1
  local deadline=$((SECONDS + BOX_WAIT_SECONDS))
  until curl -s -o /dev/null "$PANEL_HTTP/"; do
    [ "$SECONDS" -lt "$deadline" ] || return 1
    sleep "$POLL_SECONDS"
  done
}
box_up || fail "sshbox 没起来"
# 面板上先签一个别的令牌，再用它建一个入站：接管之后这两样都要原样还在
SEED_TOKEN="$(box_exec "$XUI_BIN" setting -tokenName "$SEED_TOKEN_NAME" -tokenScope admin -getApiToken | sed -n 's/^apiToken: //p' | tr -d '\r')"
[ -n "$SEED_TOKEN" ] || fail "面板签发初始令牌失败"
# panel <method> <根地址> <路径> [json]：用初始令牌直接调面板
panel() {
  local args=(-sk -X "$1" -H "Authorization: Bearer $SEED_TOKEN" -H 'Content-Type: application/json')
  [ -n "${4:-}" ] && args+=(--data "$4")
  curl "${args[@]}" "$2/panel/api$3"
}
panel POST "$PANEL_HTTP" /inbounds/add "$(jq -nc --argjson p "$INBOUND_PORT" --arg r "$INBOUND_REMARK" '{enable:true,remark:$r,listen:"",port:$p,protocol:"vmess",expiryTime:0,total:0,
  settings:{clients:[]},streamSettings:{network:"ws",security:"none",wsSettings:{path:"/it14"}},sniffing:{enabled:false}}')" | jq -e .success >/dev/null || fail "面板上预建入站失败"

# ---- 2. hub-server（允许回环地址、任务 1 秒领取一次）与超管 ----
sed -e 's/^  allowLoopback: .*/  allowLoopback: true/' -e "s/^  pollInterval: .*/  pollInterval: $POLL_INTERVAL/" \
  "$REPO_ROOT/v2-hub-server/etc/hub-server.yaml" > "$WORK/hub-server.yaml"
grep -q 'allowLoopback: true' "$WORK/hub-server.yaml" && grep -q "pollInterval: $POLL_INTERVAL" "$WORK/hub-server.yaml" || fail "临时配置没有改到回环开关或轮询间隔"
printf '%s\n' "$CRED" | (cd "$REPO_ROOT/v2-hub-server" && make -s admin-create USERNAME="$ROOT_USER") >/tmp/it-admin-create.log 2>&1 || fail "admin-create 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml"
HUB_JAR="$WORK/hub.jar"
: > "$WORK/responses.log"
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
# call <cookie jar> <csrf> <method> <path> [json] → 打印响应体；状态码写入 $WORK/code；每个响应体都留档，结尾统一查有没有泄露
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
CSRF="$(call "$HUB_JAR" "" POST /api/admin/auth/login "$(login_body_of "$ROOT_USER" "$CRED")" | jq -r '.data.csrfToken // empty')"
[ -n "$CSRF" ] || fail "hub 登录失败"
hub() { call "$HUB_JAR" "$CSRF" "$@"; }
# wait_task <任务 id>：等任务结束，打印最终状态
wait_task() {
  local deadline=$((SECONDS + TASK_WAIT_SECONDS)) status
  while :; do
    status="$(hub GET "/api/admin/host-tasks/$1" | jq -r '.data.status // empty')"
    case "$status" in queued|running) ;; *) printf '%s' "$status"; return 0 ;; esac
    [ "$SECONDS" -lt "$deadline" ] || { printf 'timeout'; return 0; }
    sleep "$POLL_SECONDS"
  done
}
# run_action <动作> <期望状态> [期望原因]：发起动作并等它结束
run_action() {
  local id status
  id="$(hub POST "/api/admin/server-hosts/$HOST_ID/actions" "$(jq -nc --arg a "$1" '{action:$a}')" | jq -r '.data.id // empty')"
  expect_code 200 "发起 $1"
  status="$(wait_task "$id")"
  hub GET "/api/admin/host-tasks/$id" > "$WORK/task.json"
  [ "$status" = "$2" ] || fail "$1 应 $2，实际 $status：$(jq -c '{errorCode:.data.errorCode,lastError:.data.lastError,log:.data.log}' "$WORK/task.json")"
  [ -z "${3:-}" ] || [ "$(jq -r .data.errorCode "$WORK/task.json")" = "$3" ] || fail "$1 的原因应是 $3：$(jq -c .data "$WORK/task.json")"
}
server_field() { hub GET "/api/admin/servers/$SERVER_ID" | jq -r "$1"; }

# ---- 3. 凭据：私钥只写不读 ----
hub POST /api/admin/ssh-credentials '{"name":"bad","username":"root","authType":"key","secret":"not a key"}' >/dev/null
expect_error 400 invalid_argument "私钥格式不对"
CRED_ID="$(hub POST /api/admin/ssh-credentials "$(jq -nc --arg n "$CRED_NAME" --rawfile k "$WORK/id" '{name:$n,username:"root",authType:"key",secret:$k}')" | jq -r '.data.id // empty')"
expect_code 200 "新建凭据"
jq -e '.data.keyFingerprint | startswith("SHA256:")' "$WORK/body" >/dev/null || fail "凭据应带公钥指纹：$(cat "$WORK/body")"
[ "$(hub GET /api/admin/ssh-credentials | jq -c '.data[0] | has("secret") or has("passphrase")')" = "false" ] || fail "凭据列表不应有 secret / passphrase 字段"

# ---- 4. 连接测试：主机密钥指纹、已有面板、会走接管；不落库 ----
hub POST /api/admin/server-hosts/test "$(jq -nc --arg a "$HOST_ADDR" --argjson p "$SSH_PORT" --argjson c "$CRED_ID" '{address:$a,sshPort:$p,credentialId:$c,providerType:"xui"}')" > "$WORK/detect.json"
expect_code 200 "连接测试"
[ "$(jq -c '[.data.osId, .data.isPrivileged, .data.panelState, .data.plan, .data.reason]' "$WORK/detect.json")" = '["debian",true,"standard","adopt",""]' ] \
  || fail "检测结果不对：$(jq -c .data "$WORK/detect.json")"
HOST_KEY="$(jq -r .data.hostKeySha256 "$WORK/detect.json")"
case "$HOST_KEY" in SHA256:*) ;; *) fail "连接测试应返回主机密钥指纹：$HOST_KEY" ;; esac
[ "$(hub GET /api/admin/server-hosts | jq '.data | length')" = "0" ] || fail "连接测试不应保存主机"

# ---- 5. 建主机：部署任务走接管 ----
BATCH="$(jq -nc --arg a "$HOST_ADDR" --arg n "$HOST_NAME" --argjson p "$SSH_PORT" --argjson c "$CRED_ID" '{credentialId:$c,sshPort:$p,providerType:"xui",hosts:[{address:$a,name:$n}]}')"
hub POST /api/admin/server-hosts "$BATCH" > "$WORK/hosts.json"
expect_code 200 "新建主机"
HOST_ID="$(jq -r '.data[0].id' "$WORK/hosts.json")"
TASK_ID="$(jq -r '.data[0].lastTask.id' "$WORK/hosts.json")"
[ "$(jq -r '.data[0].lastTask.kind' "$WORK/hosts.json")" = "provision" ] || fail "新主机应有一条部署任务：$(cat "$WORK/hosts.json")"
hub POST /api/admin/server-hosts "$BATCH" >/dev/null; expect_error 409 conflict "同名同地址的主机"
[ "$(wait_task "$TASK_ID")" = "succeeded" ] || fail "部署任务应成功：$(hub GET "/api/admin/host-tasks/$TASK_ID" | jq -c '{status:.data.status,errorCode:.data.errorCode,lastError:.data.lastError,log:.data.log}')"
hub GET "/api/admin/host-tasks/$TASK_ID" > "$WORK/task.json"
[ "$(jq -r .data.step "$WORK/task.json")" = "register" ] || fail "接管应停在 register 步：$(jq -c .data "$WORK/task.json")"
jq -e '.data.log | contains("明文 HTTP")' "$WORK/task.json" >/dev/null || fail "明文 HTTP 的面板应在日志里提示"
hub GET "/api/admin/server-hosts/$HOST_ID" > "$WORK/host.json"
SERVER_ID="$(jq -r '.data.serverId // empty' "$WORK/host.json")"
[ -n "$SERVER_ID" ] || fail "主机应已关联服务器：$(cat "$WORK/host.json")"
[ "$(jq -r .data.hostKeySha256 "$WORK/host.json")" = "$HOST_KEY" ] || fail "主机密钥应已固定为连接测试看到的那个"
[ "$(jq -c '[.data.osId, .data.serverName]' "$WORK/host.json")" = "[\"debian\",\"$HOST_NAME\"]" ] || fail "主机应记下系统并显示服务器名：$(jq -c .data "$WORK/host.json")"
for _ in $(seq 1 20); do [ "$(server_field .data.status)" = "online" ] && break; sleep "$POLL_SECONDS"; done
hub GET "/api/admin/servers/$SERVER_ID" > "$WORK/server.json"
[ "$(jq -c '[.data.name, .data.status, .data.baseUrl, .data.tlsMode, .data.hostId, .data.inboundCount]' "$WORK/server.json")" = "[\"$HOST_NAME\",\"online\",\"$PANEL_HTTP\",\"verify\",$HOST_ID,1]" ] \
  || fail "接入后的服务器不对：$(jq -c .data "$WORK/server.json")"
[ "$(hub GET "/api/admin/servers/$SERVER_ID/inbounds" | jq -r '.data[0].remark')" = "$INBOUND_REMARK" ] || fail "面板原有的入站应已同步：$(cat "$WORK/body")"
# 面板侧：多了 v2-hub 令牌；原有令牌还在而且还能用（这次查询用的就是它）
[ "$(panel GET "$PANEL_HTTP" /setting/apiTokens | jq -c '[.obj[].name] | sort')" = "[\"$SEED_TOKEN_NAME\",\"$PLATFORM_TOKEN_NAME\"]" ] \
  || fail "面板上应只有原有令牌与 v2-hub 令牌：$(panel GET "$PANEL_HTTP" /setting/apiTokens | jq -c '[.obj[].name]')"
hub POST "/api/admin/server-hosts/$HOST_ID/actions" '{"action":"provision"}' >/dev/null; expect_error 409 conflict "已接入的主机再部署"
hub POST "/api/admin/server-hosts/$HOST_ID/actions" '{"action":"shell"}' >/dev/null; expect_error 400 invalid_argument "不存在的动作"

# ---- 6. 运维动作；一台主机同时只有一条未结束的任务 ----
run_action diagnose succeeded
jq -e '.data.log | contains("服务状态: active")' "$WORK/task.json" >/dev/null || fail "查看状态的输出应在任务日志里：$(jq -r .data.log "$WORK/task.json")"
FIRST="$(hub POST "/api/admin/server-hosts/$HOST_ID/actions" '{"action":"restart_panel"}' | jq -r '.data.id // empty')"
expect_code 200 "发起重启面板"
hub POST "/api/admin/server-hosts/$HOST_ID/actions" '{"action":"restart_core"}' >/dev/null; expect_error 409 conflict "未结束时再发起动作"
hub DELETE "/api/admin/server-hosts/$HOST_ID" >/dev/null; expect_error 409 conflict "未结束时删主机"
[ "$(wait_task "$FIRST")" = "succeeded" ] || fail "重启面板应成功：$(hub GET "/api/admin/host-tasks/$FIRST" | jq -c '{errorCode:.data.errorCode,lastError:.data.lastError,log:.data.log}')"
[ "$(server_field .data.status)" = "online" ] || fail "重启后服务器应在线"
run_action restart_core succeeded
run_action reissue_token succeeded
[ "$(panel GET "$PANEL_HTTP" /setting/apiTokens | jq -c '[.obj[].name] | sort')" = "[\"$SEED_TOKEN_NAME\",\"$PLATFORM_TOKEN_NAME\"]" ] || fail "重签只轮换 v2-hub 令牌，不应多出别的"
hub POST "/api/admin/servers/$SERVER_ID/sync" >/dev/null; expect_code 200 "重签后用新令牌同步"
[ "$(hub GET "/api/admin/server-hosts/$HOST_ID/tasks" | jq -c '[.data[].kind]')" = '["reissue_token","restart_core","restart_panel","diagnose","provision"]' ] \
  || fail "任务历史应新的在前：$(cat "$WORK/body")"

# ---- 7. 配置证书：https + pin；重置面板登录口令 ----
run_action enable_tls succeeded
hub GET "/api/admin/servers/$SERVER_ID" > "$WORK/server.json"
[ "$(jq -c '[.data.status, .data.baseUrl, .data.tlsMode, (.data.certPinSha256 | length)]' "$WORK/server.json")" = "[\"online\",\"$PANEL_HTTPS\",\"pin\",44]" ] \
  || fail "配置证书后的服务器不对：$(jq -c .data "$WORK/server.json")"
[ "$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$PANEL_HTTP/" || true)" != "200" ] || fail "配置证书后面板不应再用明文 HTTP 提供页面"
hub POST "/api/admin/servers/$SERVER_ID/sync" >/dev/null; expect_code 200 "按固定指纹同步"
run_action enable_tls failed command_failed
hub POST "/api/admin/server-hosts/$HOST_ID/panel-login/reset" > "$WORK/login.json"
expect_code 200 "重置面板登录口令"
PANEL_USER="$(jq -r .data.username "$WORK/login.json")"; PANEL_PW="$(jq -r .data.password "$WORK/login.json")"
[ -n "$PANEL_USER" ] && [ "${#PANEL_PW}" -ge 16 ] || fail "应返回新的用户名与口令"
PJAR="$WORK/panel.jar"
PCSRF="$(curl -sk -b "$PJAR" -c "$PJAR" "$PANEL_HTTPS/csrf-token" | jq -r .obj)"
curl -sk -b "$PJAR" -c "$PJAR" -X POST "$PANEL_HTTPS/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $PCSRF" \
  --data "$(login_body_of "$PANEL_USER" "$PANEL_PW")" | jq -e .success >/dev/null || fail "新口令应能登录面板"

# ---- 8. 权限：server:write 不等于 host:write ----
hub POST /api/admin/roles '{"code":"it14_server_admin","name":"IT14 服务器管理","permissions":["server:read","server:write"]}' >/dev/null; expect_code 200 "建角色"
hub POST /api/admin/admins "$(jq -nc --arg u "$LIMITED_USER" --arg p "$CRED" '{username:$u,password:$p,roles:["it14_server_admin"]}')" >/dev/null; expect_code 200 "建管理员"
LJAR="$WORK/limited.jar"
LCSRF="$(call "$LJAR" "" POST /api/admin/auth/login "$(login_body_of "$LIMITED_USER" "$CRED")" | jq -r '.data.csrfToken // empty')"
call "$LJAR" "$LCSRF" GET /api/admin/server-hosts >/dev/null; expect_error 403 permission_denied "没有 host:read 看主机"
call "$LJAR" "$LCSRF" POST "/api/admin/server-hosts/$HOST_ID/actions" '{"action":"restart_panel"}' >/dev/null; expect_error 403 permission_denied "没有 host:write 发起动作"
call "$LJAR" "$LCSRF" POST "/api/admin/server-hosts/$HOST_ID/panel-login/reset" >/dev/null; expect_error 403 permission_denied "没有 host:write 重置口令"

# ---- 9. 机器重装（重建容器）：主机密钥变了，任务被拒；重置后恢复 ----
box_up || fail "sshbox 重建失败"
run_action diagnose failed host_key_changed
hub POST "/api/admin/server-hosts/$HOST_ID/reset-host-key" >/dev/null; expect_code 200 "重置主机密钥"
[ "$(jq -r .data.hostKeySha256 "$WORK/body")" = "" ] || fail "重置后主机密钥应为空"
run_action diagnose succeeded
NEW_KEY="$(hub GET "/api/admin/server-hosts/$HOST_ID" | jq -r .data.hostKeySha256)"
case "$NEW_KEY" in SHA256:*) ;; *) fail "重置后应重新记录主机密钥：$NEW_KEY" ;; esac
[ "$NEW_KEY" != "$HOST_KEY" ] || fail "重建后的主机密钥应与原来不同"

# ---- 10. 审计与泄露检查 ----
hub GET "/api/admin/audit-logs?pageSize=$AUDIT_PAGE_SIZE" > "$WORK/audit.json"
for action in ssh_credential.create host.create host_task.create host.panel_login_reset host.reset_host_key server.create server.update; do
  jq -e --arg a "$action" '[.data.items[] | select(.action == $a)] | length > 0' "$WORK/audit.json" >/dev/null || fail "审计里应有 $action"
done
for id in $(hub GET "/api/admin/server-hosts/$HOST_ID/tasks" | jq -r '.data[].id'); do hub GET "/api/admin/host-tasks/$id" >/dev/null; done
KEY_BODY="$(sed -n '2p' "$WORK/id")"   # 私钥正文的第一行
for secret in "PRIVATE KEY" "$KEY_BODY" "$SEED_TOKEN" "apiToken"; do
  ! grep -qF -- "$secret" "$WORK/responses.log" || fail "响应或任务日志里出现了不该有的内容：${secret:0:12}…"
done
# 面板口令只允许出现在重置口令的那一次响应里
[ "$(grep -cF -- "$PANEL_PW" "$WORK/responses.log")" = "1" ] || fail "面板口令应只出现在重置的那一次响应里"
# 平台签发的令牌是 48 位字母数字：任务日志里不应有这样的串
! jq -r '.data.log // empty' "$WORK/task.json" | grep -qE '[A-Za-z0-9]{48}' || fail "任务日志里有疑似令牌的内容"
! grep -qE '"log":"[^"]*[A-Za-z0-9]{48}' "$WORK/responses.log" || fail "任务日志里有疑似令牌的内容"

echo "IT-14 PASS"
