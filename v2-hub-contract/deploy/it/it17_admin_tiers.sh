#!/usr/bin/env bash
# IT-17 管理员分级加固（docs/08，docs/03 §4 – §4.4、§5.1、§5.6、§5.22，Ruling-95 – Ruling-100、Ruling-105 – Ruling-107）：
# → 超管强制两步验证：新建的超管未绑定时业务接口 403 totp_setup_required，豁免接口（me、登出、TOTP 生成 / 确认、改自己密码）可用；
#   绑定后正常；超管关闭两步验证 409
# → 超管专属与预设角色：含 admin:* / host:* 的角色 400；四个预设角色在、内置、不可改不可删；持预设“财务”的管理员碰管理员与 SSH 托管 403；
#   权限码列表带 superOnly
# → 本人改密码：旧密码错 400；成功后另一个会话 401、当前会话照常；对自己调重置密码接口 409
# → 所有者：别的超管对所有者禁用 / 重置密码 / 清两步验证都 403；命令行 admin-owner-set 转移后规则对调；
#   admin-totp-reset 后该超管重新被强制绑定
# → 改面板地址不带 token 400（启用中、停用后都一样），带 token 通过；改 TLS 校验方式不带 token 同样 400（Ruling-107）
# → 密码凭据用到新的（地址，端口）：不带 credentialSecret 400、带错 400、带对通过；私钥凭据不受影响；
#   连接测试用密码凭据一律要 credentialSecret，目标已有主机也一样（Ruling-107）
# → 门户地址与订阅地址只有超管能改：持预设“合伙人”的管理员改这两项 403 且整体不保存，改别的设置项照常（Ruling-106）
# → 短 reauthWindow：过期后高危接口 403 reauth_required，验证失败 400，reauth 后成功；没有权限码的人只得到 permission_denied；
#   员工改用户邮箱过期时 403 reauth_required 且邮箱不变，只改备注照常，验证后可改（Ruling-106）
# → 并发错密码登录：被真正校验（401）的次数不超过 auth.loginMaxFailures，其余 429（Ruling-105；专用账号 + X-Forwarded-For 区分来源）
# 黑盒，只用 curl / jq / docker / ssh-keygen / python3（TOTP）。主机只建到落库与排队这一层：地址用本机未监听的端口，
# 部署任务连不上就失败，不需要真实 SSH 目标，密码也不会发出去。
# 注意：会重建 xui-a 容器；hub-server 中途换一份短有效期的配置重启一次。
source "$(dirname "$0")/common.sh"
set -e

DEPLOY_DIR="$(cd "$IT_DIR/.." && pwd)"
XUI_URL="http://127.0.0.1:${XUI_A_PORT:-12053}"
XUI_URL_MOVED="http://localhost:${XUI_A_PORT:-12053}"   # 同一个面板换个写法，平台按“面板地址变了”处理
BASE=http://127.0.0.1:8080
OWNER=it17-owner     # 与前面用例的超管区分（run_all 只在开头重置一次库）
SECOND=it17-second
FINANCE=it17-finance
PARTNER=it17-partner  # 持预设“合伙人”：有 setting:write、user:write，但不是超管
BRUTE=it17-brute      # 并发错密码登录专用，会被锁住，后面不再用
END_USER=it17-mailuser
MAIL_DOMAIN=it17.example.com
PORTAL_URL_NEW=https://it17-portal.example.com
SUB_BASE_URL_NEW=https://it17-sub.example.com
BRUTE_IP=198.51.100.17    # TEST-NET-2：经 X-Forwarded-For 给并发登录一个独立来源（etc/hub-server.yaml 采信 127.0.0.1 的转发头）
BRUTE_TRIES=20
CRED="it-$(openssl rand -hex 12)"
CRED_NEW="it-$(openssl rand -hex 12)"
SSH_CRED_PW="it-$(openssl rand -hex 12)"
HOST_ADDR=127.0.0.1
HOST_PORT_PW=1        # 本机没人监听的端口：部署任务连不上即失败
HOST_PORT_KEY=2
SHORT_WINDOW=5s
SHORT_WINDOW_WAIT=7
SUPER_ONLY='["admin:read","admin:write","host:read","host:write"]'
PRESET_ROLES='["finance","operator","ops","partner"]'
FINANCE_PERMS='["finance:read","finance:write","plan:read","plan:write","shop:read","shop:write","user:read"]'
WORK="$(mktemp -d /tmp/it17.XXXXXX)"
trap 'rm -rf "$WORK"; stop_all' EXIT

fail() { echo "IT-17: $*"; exit 1; }

# ---- 1. 全新 3x-ui（改地址那一段要一台真实面板），签发 API token ----
XUI_JAR="$WORK/xui.jar"
(cd "$DEPLOY_DIR" && docker compose -f docker-compose.yml --profile xui up -d --force-recreate --renew-anon-volumes xui-a >/dev/null 2>&1) || fail "启动 xui-a 失败"
xui_user="" xui_pw=""
for _ in $(seq 1 30); do
  logs="$(docker compose -f "$DEPLOY_DIR/docker-compose.yml" logs xui-a 2>/dev/null)"
  xui_user="$(printf '%s' "$logs" | sed -n 's/.*Username: \([^ ]*\) .*/\1/p' | tail -1)"
  xui_pw="$(printf '%s' "$logs" | sed -n 's/.*Password: \([^ ]*\) .*/\1/p' | tail -1)"
  [ -n "$xui_user" ] && curl -s -o /dev/null "$XUI_URL/csrf-token" && break
  sleep 1
done
[ -n "$xui_user" ] || fail "没有在 xui-a 日志里找到初始账号"
xui_csrf() { curl -s -b "$XUI_JAR" -c "$XUI_JAR" "$XUI_URL/csrf-token" | jq -r .obj; }
curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/login" -H 'Content-Type: application/json' -H "X-CSRF-Token: $(xui_csrf)" \
  --data "$(jq -nc --arg u "$xui_user" --arg p "$xui_pw" '{username:$u,password:$p}')" | jq -e .success >/dev/null || fail "3x-ui 登录失败"
XUI_TOKEN="$(curl -s -b "$XUI_JAR" -c "$XUI_JAR" -X POST "$XUI_URL/panel/api/setting/apiTokens/create" -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: $(xui_csrf)" --data '{"name":"it17","scope":"admin","expiresAt":0}' | jq -r '.obj.token // empty')"
[ -n "$XUI_TOKEN" ] || fail "签发 3x-ui API token 失败"

# ---- 2. hub-server（有效期拉长、允许回环地址）与所有者 ----
it_hub_config "$WORK/hub-server.yaml" -e 's/^  allowLoopback: .*/  allowLoopback: true/' || fail "派生配置失败"
IT_REAUTH_WINDOW="$SHORT_WINDOW" it_hub_config "$WORK/hub-server-short.yaml" -e 's/^  allowLoopback: .*/  allowLoopback: true/' \
  || fail "派生短有效期配置失败"
grep -q 'allowLoopback: true' "$WORK/hub-server.yaml" || fail "临时配置没有改到回环开关"
it_admin_cli admin-create "$OWNER" "$CRED" || fail "admin-create 失败"
# run_all 里前面的用例已经有了所有者（最早建的超管），这里显式转给本用例的超管，不依赖执行顺序
it_admin_cli admin-owner-set "$OWNER" || fail "admin-owner-set $OWNER 失败"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server.yaml"

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
login_body_of() { jq -nc --arg u "$1" --arg p "$2" '{username:$u,password:$p}'; }
csrf_of_body() { jq -r '.data.csrfToken // empty' "$WORK/body"; }
admin_id_of() { call "$1" "" GET /api/admin/auth/me | jq -r .data.admin.id; }
disable_body() { jq -nc '{roles:["super_admin"],isDisabled:true}'; }
reset_pw_body() { jq -nc --arg p "it-$(openssl rand -hex 12)" '{password:$p}'; }

# ---- 3. R10：新建的超管未绑定两步验证 ----
OJAR="$WORK/owner.jar"
OJAR2="$WORK/owner2.jar"
call "$OJAR2" "" POST /api/admin/auth/login "$(login_body_of "$OWNER" "$CRED")" >/dev/null; expect_code 200 "未绑定的超管用密码登录"
jq -e '.data | .isSuperAdmin and .isOwner and .totpSetupRequired and (.totpEnabled | not)' "$WORK/body" >/dev/null \
  || fail "会话信息应标明超管、所有者、须绑定：$(cat "$WORK/body")"
O2CSRF="$(csrf_of_body)"
call "$OJAR2" "$O2CSRF" POST /api/admin/auth/logout >/dev/null; expect_code 200 "未绑定时登出（豁免）"
call "$OJAR" "" POST /api/admin/auth/login "$(login_body_of "$OWNER" "$CRED")" >/dev/null; expect_code 200 "未绑定的超管再登录"
OCSRF="$(csrf_of_body)"
call "$OJAR" "$OCSRF" GET /api/admin/auth/me >/dev/null; expect_code 200 "未绑定时 me（豁免）"
call "$OJAR" "$OCSRF" GET /api/admin/servers >/dev/null; expect_error 403 totp_setup_required "未绑定时看服务器"
call "$OJAR" "$OCSRF" GET /api/admin/admins >/dev/null; expect_error 403 totp_setup_required "未绑定时看管理员"
call "$OJAR" "$OCSRF" GET /api/admin/dashboard >/dev/null; expect_error 403 totp_setup_required "未绑定时看总览（只需登录的接口）"
# 改自己密码豁免：旧密码错得到 400（参数层面的拒绝），说明请求没有被 totp_setup_required 挡住
call "$OJAR" "$OCSRF" PUT /api/admin/auth/password "$(jq -nc --arg o "wrong-$CRED" --arg n "$CRED_NEW" '{oldPassword:$o,newPassword:$n}')" >/dev/null
expect_error 400 invalid_argument "未绑定时改自己密码、旧密码错"
it_admin_totp_bind "$OJAR" "$OCSRF" "$OWNER" || fail "绑定两步验证失败"
call "$OJAR" "$OCSRF" GET /api/admin/servers >/dev/null; expect_code 200 "绑定后看服务器"
[ "$(call "$OJAR" "" GET /api/admin/auth/me | jq -c '[.data.totpEnabled, .data.totpSetupRequired]')" = '[true,false]' ] || fail "绑定后会话信息不对：$(cat "$WORK/body")"
# 关闭：超管一律 409；验证码用当前步长算一个格式正确的（409 在校验验证码之前，不占步长）
DISABLE_CODE="$(it_totp_code "$(it_totp_key "$OWNER")" "$(it_totp_now_step)")"
call "$OJAR" "$OCSRF" POST /api/admin/auth/totp/disable "$(jq -nc --arg c "$DISABLE_CODE" '{code:$c}')" >/dev/null
expect_error 409 conflict "超管关闭两步验证"
OWNER_ID="$(admin_id_of "$OJAR")"
owner() { call "$OJAR" "$OCSRF" "$@"; }

# ---- 4. 超管专属权限码与预设角色（R1、R2、R6）----
owner POST /api/admin/roles '{"code":"it17_admin_bad","name":"含管理员写","permissions":["admin:write"]}' >/dev/null
expect_error 400 invalid_argument "角色含 admin:write"
owner POST /api/admin/roles '{"code":"it17_host_bad","name":"含主机读","permissions":["user:read","host:read"]}' >/dev/null
expect_error 400 invalid_argument "角色含 host:read"
owner GET /api/admin/permissions > "$WORK/perms.json"; expect_code 200 "权限码列表"
jq -e '.data | length > 0 and all(has("superOnly"))' "$WORK/perms.json" >/dev/null || fail "每个权限码都应带 superOnly：$(cat "$WORK/perms.json")"
[ "$(jq -c '[.data[] | select(.superOnly) | .code] | sort' "$WORK/perms.json")" = "$SUPER_ONLY" ] \
  || fail "超管专属应正好是 admin:* 与 host:*：$(jq -c '[.data[] | select(.superOnly) | .code]' "$WORK/perms.json")"
owner GET /api/admin/roles > "$WORK/roles.json"; expect_code 200 "角色列表"
[ "$(jq -c '[.data[] | select(.isBuiltin and .code != "super_admin") | .code] | sort' "$WORK/roles.json")" = "$PRESET_ROLES" ] \
  || fail "四个预设角色应都在且是内置：$(jq -c '[.data[] | {code, isBuiltin}]' "$WORK/roles.json")"
[ "$(jq -c '.data[] | select(.code == "finance") | .permissions | sort' "$WORK/roles.json")" = "$FINANCE_PERMS" ] \
  || fail "财务角色的权限码不对：$(jq -c '.data[] | select(.code == "finance")' "$WORK/roles.json")"
jq -e '.data[] | select(.code == "partner") | .permissions | any(. == "user:write") and all(startswith("admin:") or startswith("host:") | not)' \
  "$WORK/roles.json" >/dev/null || fail "合伙人应有全部非超管专属的权限码：$(jq -c '.data[] | select(.code == "partner")' "$WORK/roles.json")"
FINANCE_ROLE_ID="$(jq -r '.data[] | select(.code == "finance") | .id' "$WORK/roles.json")"
owner PUT "/api/admin/roles/$FINANCE_ROLE_ID" '{"name":"财务改名","permissions":["user:read"]}' >/dev/null; expect_error 409 conflict "改预设角色"
owner DELETE "/api/admin/roles/$FINANCE_ROLE_ID" >/dev/null; expect_error 409 conflict "删预设角色"
owner POST /api/admin/admins "$(jq -nc --arg u "$FINANCE" --arg p "$CRED" '{username:$u,password:$p,roles:["finance"]}')" >/dev/null
expect_code 200 "建财务管理员"
FJAR="$WORK/finance.jar"
call "$FJAR" "" POST /api/admin/auth/login "$(login_body_of "$FINANCE" "$CRED")" >/dev/null; expect_code 200 "财务登录（非超管不强制两步验证）"
FCSRF="$(csrf_of_body)"
call "$FJAR" "$FCSRF" GET /api/admin/orders >/dev/null; expect_code 200 "财务看订单"
call "$FJAR" "$FCSRF" GET /api/admin/admins >/dev/null; expect_error 403 permission_denied "财务看管理员列表"
call "$FJAR" "$FCSRF" GET /api/admin/permissions >/dev/null; expect_error 403 permission_denied "财务看权限码"
call "$FJAR" "$FCSRF" GET /api/admin/ssh-credentials >/dev/null; expect_error 403 permission_denied "财务看 SSH 凭据"
call "$FJAR" "$FCSRF" GET /api/admin/server-hosts >/dev/null; expect_error 403 permission_denied "财务看主机"

# ---- 5. 本人改密码（R9、R7）----
it_admin_login "$OJAR2" "$OWNER" "$CRED" >/dev/null || fail "所有者第二个会话登录失败"
call "$OJAR2" "" GET /api/admin/auth/me >/dev/null; expect_code 200 "第二个会话 me"
owner PUT /api/admin/auth/password "$(jq -nc --arg o "wrong-$CRED" --arg n "$CRED_NEW" '{oldPassword:$o,newPassword:$n}')" >/dev/null
expect_error 400 invalid_argument "改密码时旧密码错"
owner PUT /api/admin/auth/password "$(jq -nc --arg o "$CRED" --arg n "$CRED_NEW" '{oldPassword:$o,newPassword:$n}')" >/dev/null
expect_code 200 "改自己密码"
call "$OJAR2" "" GET /api/admin/auth/me >/dev/null; expect_code 401 "改密码后另一个会话"
owner GET /api/admin/auth/me >/dev/null; expect_code 200 "改密码后当前会话"
CRED_OWNER="$CRED_NEW"

# ---- 6. 所有者（R11、R4）----
owner POST /api/admin/admins "$(jq -nc --arg u "$SECOND" --arg p "$CRED" '{username:$u,password:$p,roles:["super_admin"]}')" >/dev/null
expect_code 200 "建第二个超管"
SJAR="$WORK/second.jar"
SCSRF="$(it_admin_login "$SJAR" "$SECOND" "$CRED")" || fail "第二个超管登录失败"
SECOND_ID="$(admin_id_of "$SJAR")"
second() { call "$SJAR" "$SCSRF" "$@"; }
second PUT "/api/admin/admins/$SECOND_ID/password" "$(reset_pw_body)" >/dev/null; expect_error 409 conflict "对自己调重置密码接口"
second PUT "/api/admin/admins/$OWNER_ID" "$(disable_body)" >/dev/null; expect_error 403 permission_denied "别的超管禁用所有者"
second PUT "/api/admin/admins/$OWNER_ID/password" "$(reset_pw_body)" >/dev/null; expect_error 403 permission_denied "别的超管重置所有者密码"
second DELETE "/api/admin/admins/$OWNER_ID/totp" >/dev/null; expect_error 403 permission_denied "别的超管清所有者的两步验证"
owner GET /api/admin/auth/me >/dev/null; expect_code 200 "所有者会话不受影响"

it_admin_cli admin-owner-set "$SECOND" || fail "admin-owner-set $SECOND 失败"
[ "$(second GET /api/admin/auth/me | jq -r .data.isOwner)" = "true" ] || fail "转移后第二个超管应是所有者：$(cat "$WORK/body")"
[ "$(owner GET /api/admin/auth/me | jq -r .data.isOwner)" = "false" ] || fail "转移后原所有者不应再是所有者：$(cat "$WORK/body")"
owner PUT "/api/admin/admins/$SECOND_ID" "$(disable_body)" >/dev/null; expect_error 403 permission_denied "转移后禁用新所有者"
owner PUT "/api/admin/admins/$SECOND_ID/password" "$(reset_pw_body)" >/dev/null; expect_error 403 permission_denied "转移后重置新所有者密码"
owner DELETE "/api/admin/admins/$SECOND_ID/totp" >/dev/null; expect_error 403 permission_denied "转移后清新所有者的两步验证"
second DELETE "/api/admin/admins/$OWNER_ID/totp" >/dev/null; expect_code 200 "新所有者清原所有者的两步验证"
owner GET /api/admin/auth/me >/dev/null; expect_code 401 "被清两步验证后原会话失效"
it_totp_forget "$OWNER"
# 被清除的超管再登录：密码即可登录，但须重新绑定才能调业务接口
call "$OJAR" "" POST /api/admin/auth/login "$(login_body_of "$OWNER" "$CRED_OWNER")" >/dev/null; expect_code 200 "被清两步验证后登录"
OCSRF="$(csrf_of_body)"
owner GET /api/admin/servers >/dev/null; expect_error 403 totp_setup_required "被清两步验证后看服务器"
it_admin_totp_bind "$OJAR" "$OCSRF" "$OWNER" || fail "重新绑定两步验证失败"
owner GET /api/admin/servers >/dev/null; expect_code 200 "重新绑定后看服务器"

# 命令行清所有者的两步验证（遗失设备）：会话全部失效，下次登录后重新被强制绑定
it_admin_cli admin-totp-reset "$SECOND" || fail "admin-totp-reset $SECOND 失败"
it_totp_forget "$SECOND"
second GET /api/admin/auth/me >/dev/null; expect_code 401 "admin-totp-reset 后原会话失效"
call "$SJAR" "" POST /api/admin/auth/login "$(login_body_of "$SECOND" "$CRED")" >/dev/null; expect_code 200 "admin-totp-reset 后登录"
jq -e '.data.totpSetupRequired' "$WORK/body" >/dev/null || fail "admin-totp-reset 后应须重新绑定：$(cat "$WORK/body")"
SCSRF="$(csrf_of_body)"
second GET /api/admin/servers >/dev/null; expect_error 403 totp_setup_required "admin-totp-reset 后看服务器"
it_admin_totp_bind "$SJAR" "$SCSRF" "$SECOND" || fail "admin-totp-reset 后重新绑定失败"
second GET /api/admin/servers >/dev/null; expect_code 200 "admin-totp-reset 后重新绑定再看服务器"

# ---- 7. 改面板地址必须重填 token（Ruling-97）----
server_body() {  # server_body <面板地址> <token> <是否停用> [TLS 校验方式，默认 verify]
  jq -nc --arg u "$1" --arg t "$2" --argjson d "$3" --arg m "${4:-verify}" '{name:"it17-xui-a",providerType:"xui",baseUrl:$u,apiToken:$t,tlsMode:$m,region:"",isDisabled:$d}'
}
SERVER_ID="$(owner POST /api/admin/servers "$(server_body "$XUI_URL" "$XUI_TOKEN" false)" | jq -r '.data.id // empty')"
expect_code 200 "接入服务器"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "" false)" >/dev/null; expect_error 400 invalid_argument "改面板地址不带 token"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL" "" true)" >/dev/null; expect_code 200 "地址不变、token 留空、停用"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "" true)" >/dev/null; expect_error 400 invalid_argument "停用后改面板地址不带 token"
[ "$(owner GET "/api/admin/servers/$SERVER_ID" | jq -r .data.baseUrl)" = "$XUI_URL" ] || fail "被拒的修改不应改动面板地址：$(cat "$WORK/body")"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "$XUI_TOKEN" true)" >/dev/null; expect_code 200 "停用后改面板地址并带 token"
[ "$(owner GET "/api/admin/servers/$SERVER_ID" | jq -r .data.baseUrl)" = "$XUI_URL_MOVED" ] || fail "面板地址应已更新：$(cat "$WORK/body")"
# Ruling-107：TLS 校验方式变了同样须重填 token（与是否停用无关），被拒不改；带 token 通过，再改回 verify 也要带
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "" true skip)" >/dev/null; expect_error 400 invalid_argument "改 TLS 校验方式不带 token"
[ "$(owner GET "/api/admin/servers/$SERVER_ID" | jq -r .data.tlsMode)" = "verify" ] || fail "被拒的修改不应改动 TLS 校验方式：$(cat "$WORK/body")"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "$XUI_TOKEN" true skip)" >/dev/null; expect_code 200 "改 TLS 校验方式并带 token"
[ "$(jq -r .data.tlsMode "$WORK/body")" = "skip" ] || fail "TLS 校验方式应已更新：$(cat "$WORK/body")"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "" true)" >/dev/null; expect_error 400 invalid_argument "改回 verify 不带 token"
owner PUT "/api/admin/servers/$SERVER_ID" "$(server_body "$XUI_URL_MOVED" "$XUI_TOKEN" true)" >/dev/null; expect_code 200 "改回 verify 并带 token"

# ---- 8. 密码凭据用到新的（地址，端口）须再输一次密码（Ruling-97，§5.22）；私钥凭据不受影响 ----
PW_CRED_ID="$(owner POST /api/admin/ssh-credentials "$(jq -nc --arg s "$SSH_CRED_PW" '{name:"it17-password",username:"root",authType:"password",secret:$s}')" | jq -r '.data.id // empty')"
expect_code 200 "建密码凭据"
batch_body() {  # batch_body <凭据 id> <SSH 端口> <主机名> [credentialSecret]
  jq -nc --argjson c "$1" --argjson p "$2" --arg n "$3" --arg a "$HOST_ADDR" --arg s "${4:-}" \
    '{credentialId:$c,sshPort:$p,providerType:"xui",hosts:[{address:$a,name:$n}]} + (if $s == "" then {} else {credentialSecret:$s} end)'
}
owner POST /api/admin/server-hosts/test "$(jq -nc --argjson c "$PW_CRED_ID" --argjson p "$HOST_PORT_PW" --arg a "$HOST_ADDR" '{address:$a,sshPort:$p,credentialId:$c,providerType:"xui"}')" >/dev/null
expect_error 400 invalid_argument "密码凭据连接测试不带 credentialSecret"
owner PUT "/api/admin/servers/$SERVER_ID/host" "$(jq -nc --argjson c "$PW_CRED_ID" --argjson p "$HOST_PORT_PW" --arg a "$HOST_ADDR" '{address:$a,sshPort:$p,credentialId:$c}')" >/dev/null
expect_error 400 invalid_argument "密码凭据绑定 SSH 不带 credentialSecret"
owner POST /api/admin/server-hosts "$(batch_body "$PW_CRED_ID" "$HOST_PORT_PW" it17-pw)" >/dev/null; expect_error 400 invalid_argument "密码凭据建主机不带 credentialSecret"
owner POST /api/admin/server-hosts "$(batch_body "$PW_CRED_ID" "$HOST_PORT_PW" it17-pw "wrong-$SSH_CRED_PW")" >/dev/null
expect_error 400 invalid_argument "密码凭据建主机 credentialSecret 不对"
# 只看本用例的主机名（run_all 里 IT-14 留下的主机还在）
[ "$(owner GET /api/admin/server-hosts | jq '[.data[] | select(.name == "it17-pw")] | length')" = "0" ] || fail "被拒的请求不应落库主机：$(cat "$WORK/body")"
owner POST /api/admin/server-hosts "$(batch_body "$PW_CRED_ID" "$HOST_PORT_PW" it17-pw "$SSH_CRED_PW")" >/dev/null; expect_code 200 "密码凭据建主机 credentialSecret 正确"
# Ruling-107：连接测试用密码凭据一律要 credentialSecret——目标（地址，端口）已有用这份凭据的主机也一样
owner POST /api/admin/server-hosts/test "$(jq -nc --argjson c "$PW_CRED_ID" --argjson p "$HOST_PORT_PW" --arg a "$HOST_ADDR" '{address:$a,sshPort:$p,credentialId:$c,providerType:"xui"}')" >/dev/null
expect_error 400 invalid_argument "已用过的目标做连接测试不带 credentialSecret"
ssh-keygen -q -t ed25519 -N '' -C it17 -f "$WORK/id" || fail "生成密钥对失败"
KEY_CRED_ID="$(owner POST /api/admin/ssh-credentials "$(jq -nc --rawfile k "$WORK/id" '{name:"it17-key",username:"root",authType:"key",secret:$k}')" | jq -r '.data.id // empty')"
expect_code 200 "建私钥凭据"
owner POST /api/admin/server-hosts "$(batch_body "$KEY_CRED_ID" "$HOST_PORT_KEY" it17-key)" >/dev/null; expect_code 200 "私钥凭据建主机不需要 credentialSecret"
grep -qF -- "$SSH_CRED_PW" "/tmp/it-v2-hub-server.log" && fail "hub-server 日志里出现了凭据密码"

# ---- 8.5 门户地址与订阅地址只有超管能改（Ruling-106）；顺带建好后面用的合伙人、终端用户与并发登录专用账号 ----
owner POST /api/admin/admins "$(jq -nc --arg u "$PARTNER" --arg p "$CRED" '{username:$u,password:$p,roles:["partner"]}')" >/dev/null
expect_code 200 "建合伙人管理员"
owner POST /api/admin/admins "$(jq -nc --arg u "$BRUTE" --arg p "$CRED" '{username:$u,password:$p,roles:["finance"]}')" >/dev/null
expect_code 200 "建并发登录专用的管理员"
END_USER_ID="$(owner POST /api/admin/users "$(jq -nc --arg u "$END_USER" --arg p "$CRED" --arg e "$END_USER@$MAIL_DOMAIN" '{username:$u,password:$p,email:$e}')" | jq -r '.data.id // empty')"
expect_code 200 "建终端用户"
PJAR="$WORK/partner.jar"
call "$PJAR" "" POST /api/admin/auth/login "$(login_body_of "$PARTNER" "$CRED")" >/dev/null; expect_code 200 "合伙人登录"
PCSRF="$(csrf_of_body)"
partner() { call "$PJAR" "$PCSRF" "$@"; }
settings_brief() { owner GET /api/admin/settings | jq -c '.data | [.site.name, .site.portalUrl, .subscription.baseUrl]'; }
SETTINGS_BEFORE="$(settings_brief)"
SITE_NAME="$(printf '%s' "$SETTINGS_BEFORE" | jq -r '.[0]')"
PORTAL_URL="$(printf '%s' "$SETTINGS_BEFORE" | jq -r '.[1]')"
SUB_BASE_URL="$(printf '%s' "$SETTINGS_BEFORE" | jq -r '.[2]')"
partner PUT /api/admin/settings "$(jq -nc --arg u "$PORTAL_URL_NEW" '{site:{portalUrl:$u}}')" >/dev/null; expect_error 403 permission_denied "非超管改门户地址"
partner PUT /api/admin/settings "$(jq -nc --arg u "$SUB_BASE_URL_NEW" '{subscription:{baseUrl:$u}}')" >/dev/null; expect_error 403 permission_denied "非超管改订阅地址"
partner PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME-it17" --arg u "$PORTAL_URL_NEW" '{site:{name:$n,portalUrl:$u}}')" >/dev/null
expect_error 403 permission_denied "非超管改站点名时夹带门户地址"
[ "$(settings_brief)" = "$SETTINGS_BEFORE" ] || fail "被拒的保存不应改动任何设置项：$SETTINGS_BEFORE → $(settings_brief)"
partner PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME-it17" --arg u "$PORTAL_URL" '{site:{name:$n,portalUrl:$u}}')" >/dev/null
expect_code 200 "非超管改站点名、门户地址原样提交"
[ "$(settings_brief)" = "$(jq -nc --arg n "$SITE_NAME-it17" --arg p "$PORTAL_URL" --arg b "$SUB_BASE_URL" '[$n, $p, $b]')" ] || fail "非超管改站点名应生效：$(settings_brief)"
owner PUT /api/admin/settings "$(jq -nc --arg u "$PORTAL_URL_NEW" --arg b "$SUB_BASE_URL_NEW" '{site:{portalUrl:$u},subscription:{baseUrl:$b}}')" >/dev/null
expect_code 200 "超管改门户地址与订阅地址"
[ "$(settings_brief)" = "$(jq -nc --arg n "$SITE_NAME-it17" --arg p "$PORTAL_URL_NEW" --arg b "$SUB_BASE_URL_NEW" '[$n, $p, $b]')" ] || fail "超管的修改应生效：$(settings_brief)"
# 复原：后面的用例（IT-18 取订阅地址再 curl）依赖订阅地址指向本机
owner PUT /api/admin/settings "$(jq -nc --arg n "$SITE_NAME" --arg u "$PORTAL_URL" --arg b "$SUB_BASE_URL" '{site:{name:$n,portalUrl:$u},subscription:{baseUrl:$b}}')" >/dev/null
expect_code 200 "超管复原设置"
[ "$(settings_brief)" = "$SETTINGS_BEFORE" ] || fail "设置应已复原：$SETTINGS_BEFORE → $(settings_brief)"

# ---- 9. 短有效期（R12）：换一份 reauthWindow=$SHORT_WINDOW 的配置重启 hub-server，会话在库里、Cookie 照用 ----
stop_modules
wait_port_free 8080 30 || fail "hub-server 没有退出"
start_module v2-hub-server 8080 CONFIG="$WORK/hub-server-short.yaml"
OCSRF="$(it_admin_login "$OJAR" "$OWNER" "$CRED_OWNER")" || fail "短有效期下所有者登录失败"
call "$FJAR" "" POST /api/admin/auth/login "$(login_body_of "$FINANCE" "$CRED")" >/dev/null; expect_code 200 "短有效期下财务登录"
FCSRF="$(csrf_of_body)"
# 合伙人在长有效期下登录时记下的验证有效期还没到，换配置后要重新登录才按短有效期算
call "$PJAR" "" POST /api/admin/auth/login "$(login_body_of "$PARTNER" "$CRED")" >/dev/null; expect_code 200 "短有效期下合伙人登录"
PCSRF="$(csrf_of_body)"
sleep "$SHORT_WINDOW_WAIT"
TMP_ROLE='{"code":"it17_tmp","name":"IT17 临时","permissions":["node:read"]}'
call "$FJAR" "$FCSRF" POST /api/admin/roles "$TMP_ROLE" >/dev/null; expect_error 403 permission_denied "过期后没有权限码的人调高危接口（先判权限码）"
owner POST /api/admin/roles "$TMP_ROLE" >/dev/null; expect_error 403 reauth_required "过期后调高危接口"
owner GET /api/admin/roles >/dev/null; expect_code 200 "过期后普通接口照常"
good="$(it_totp_code "$(it_totp_key "$OWNER")" "$(it_totp_now_step)")"
bad="$(printf '%06d' $(( (10#$good + 1) % 1000000 )))"
owner POST /api/admin/auth/reauth "$(jq -nc --arg c "$bad" '{totpCode:$c}')" >/dev/null; expect_error 400 invalid_argument "二次验证码错误"
owner POST /api/admin/roles "$TMP_ROLE" >/dev/null; expect_error 403 reauth_required "验证失败后仍须验证"
out="$(it_admin_reauth "$OJAR" "$OCSRF" "$OWNER")" || fail "二次验证失败：$out"
printf '%s' "${out#* }" | jq -e '.data.reauthUntil | length > 0' >/dev/null || fail "二次验证应返回有效期：$out"
owner POST /api/admin/roles "$TMP_ROLE" >/dev/null; expect_code 200 "二次验证后调高危接口"

# ---- 10. 员工改用户邮箱按内容判定是否须二次验证（Ruling-106）：合伙人的验证已过期 ----
END_EMAIL="$END_USER@$MAIL_DOMAIN"
END_EMAIL_NEW="$END_USER-new@$MAIL_DOMAIN"
user_brief() { owner GET "/api/admin/users/$END_USER_ID" | jq -c '.data | [.email, .remark]'; }
user_update_body() { jq -nc --arg r "$1" --arg e "${2:-}" '{remark:$r,isDisabled:false} + (if $e == "" then {} else {email:$e} end)'; }
partner PUT "/api/admin/users/$END_USER_ID" "$(user_update_body "" "$END_EMAIL_NEW")" >/dev/null; expect_error 403 reauth_required "验证过期后改用户邮箱"
partner PUT "/api/admin/users/$END_USER_ID" "$(jq -nc '{remark:"",isDisabled:false,email:""}')" >/dev/null; expect_error 403 reauth_required "验证过期后清空用户邮箱"
[ "$(user_brief)" = "$(jq -nc --arg e "$END_EMAIL" '[$e, ""]')" ] || fail "被拒的修改不应改动邮箱：$(user_brief)"
partner PUT "/api/admin/users/$END_USER_ID" "$(user_update_body "it17 note")" >/dev/null; expect_code 200 "验证过期后只改备注"
partner PUT "/api/admin/users/$END_USER_ID" "$(user_update_body "it17 note 2" "$END_EMAIL")" >/dev/null; expect_code 200 "验证过期后邮箱原样提交、改备注"
[ "$(user_brief)" = "$(jq -nc --arg e "$END_EMAIL" '[$e, "it17 note 2"]')" ] || fail "只改备注应生效且邮箱不变：$(user_brief)"
out="$(it_admin_reauth "$PJAR" "$PCSRF" "$PARTNER" "$CRED")" || fail "合伙人二次验证失败：$out"
partner PUT "/api/admin/users/$END_USER_ID" "$(user_update_body "it17 note 2" "$END_EMAIL_NEW")" >/dev/null; expect_code 200 "二次验证后改用户邮箱"
[ "$(user_brief)" = "$(jq -nc --arg e "$END_EMAIL_NEW" '[$e, "it17 note 2"]')" ] || fail "验证后邮箱应已更新：$(user_brief)"

# ---- 11. 并发错密码登录（Ruling-105）：失败名额在校验口令之前先占用，同时发出的一批请求也只有上限次被真正校验 ----
# 用专用账号与独立来源 IP，锁住的是它们，不影响别的用例；最后用正确密码确认已被锁定
LOGIN_MAX_FAILURES="$(sed -n 's/^  loginMaxFailures: //p' "$WORK/hub-server-short.yaml")"
[ -n "$LOGIN_MAX_FAILURES" ] || fail "没有在联调配置里读到 auth.loginMaxFailures"
BRUTE_BODY="$(login_body_of "$BRUTE" "wrong-$CRED")"
seq 1 "$BRUTE_TRIES" | xargs -P "$BRUTE_TRIES" -I{} curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' \
  -H "X-Forwarded-For: $BRUTE_IP" --data "$BRUTE_BODY" "$BASE/api/admin/auth/login" > "$WORK/brute.codes"
n401="$(grep -cx 401 "$WORK/brute.codes" || true)"
n429="$(grep -cx 429 "$WORK/brute.codes" || true)"
echo "IT-17: ${BRUTE_TRIES} 个并发错密码登录：401 × ${n401}，429 × ${n429}"
[ $((n401 + n429)) -eq "$BRUTE_TRIES" ] || fail "并发登录应只返回 401 或 429：$(sort "$WORK/brute.codes" | uniq -c | tr '\n' ' ')"
[ "$n401" -ge 1 ] && [ "$n401" -le "$LOGIN_MAX_FAILURES" ] || fail "被真正校验的次数应在 1 – ${LOGIN_MAX_FAILURES}，实际 ${n401}"
curl -s -o "$WORK/body" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "X-Forwarded-For: $BRUTE_IP" \
  --data "$(login_body_of "$BRUTE" "$CRED")" "$BASE/api/admin/auth/login" > "$WORK/code"
expect_error 429 rate_limited "并发错密码之后用正确密码登录（已锁定）"
echo "IT-17 PASS"
