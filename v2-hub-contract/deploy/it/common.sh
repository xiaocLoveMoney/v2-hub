#!/usr/bin/env bash
# IT 联调脚本公共 helper:起停模块进程、等端口、HTTP 断言。黑盒,不 import 模块代码。
# 各 itN_*.sh 以 `source "$(dirname "$0")/common.sh"` 引入。
set -uo pipefail
IT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$IT_DIR/../../.." && pwd)"
declare -a IT_PIDS=()

# start_module <目录名> <端口> [额外 env...]:后台 `make run`,等端口可用
start_module() {
  local dir="$1" port="$2"; shift 2
  # set -m 让后台任务自成进程组，stop_all 才能连同 go run / vite 的子进程一起结束
  set -m
  # 联调默认关掉服务器位置扫描，不把本机出口 IP 发给外部 IP 库；需要的用例（IT-12）自己传模拟端地址覆盖
  ( cd "$REPO_ROOT/$dir" && env HUB_GEOIP_LOOKUP_URL="" "$@" make run >"/tmp/it-$dir.log" 2>&1 ) &
  IT_PIDS+=($!)
  set +m
  wait_port "$port" 30 || { echo "start_module: $dir 在 $port 端口 30s 未就绪, 日志 /tmp/it-$dir.log"; return 1; }
}

wait_port() {  # wait_port <端口> <秒>：IPv4 或 IPv6 回环任一可连即就绪（vite 按 localhost 解析，可能只监听 ::1）
  local port="$1" n="${2:-30}"
  for ((i=0; i<n; i++)); do
    (echo >/dev/tcp/127.0.0.1/"$port") 2>/dev/null && return 0
    (echo >/dev/tcp/::1/"$port") 2>/dev/null && return 0
    sleep 1
  done
  return 1
}

stop_modules() {  # 只停模块进程（IT-17 换配置重启 hub-server 用），不清两步验证的状态
  for p in "${IT_PIDS[@]:-}"; do
    [ -n "$p" ] || continue
    kill -- "-$p" 2>/dev/null || kill "$p" 2>/dev/null || true   # 进程可能已自行退出
  done
  IT_PIDS=()
}

stop_all() {  # 脚本退出时调：停模块进程，删掉本次记下的 TOTP 密钥
  stop_modules
  [ -z "${IT_TOTP_DIR:-}" ] || rm -rf "$IT_TOTP_DIR"
}
trap stop_all EXIT

# mihomo 配置检查（IT-5 / IT-8 / IT-12）：订阅带 GEOIP 规则，mihomo -t 要先有 GeoIP 库，容器里自己去 GitHub 下载常超时。
# 改为在宿主机上下载一次并缓存（curl 读 https_proxy 等环境变量，即走系统代理），每次复制进容器的工作目录。
MIHOMO_IMAGE="${IT_MIHOMO_IMAGE:-metacubex/mihomo:latest}"
MIHOMO_GEOIP_URL="${IT_MIHOMO_GEOIP_URL:-https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb}"
MIHOMO_GEOIP_FILE="geoip.metadb"
IT_CACHE_DIR="${IT_CACHE_DIR:-$HOME/.cache/v2-hub-it}"
GEOIP_DOWNLOAD_TIMEOUT=180

# mihomo_geoip：打印缓存的 GeoIP 库路径，缓存里没有就先下载
mihomo_geoip() {
  local file="$IT_CACHE_DIR/$MIHOMO_GEOIP_FILE"
  if [ ! -s "$file" ]; then
    mkdir -p "$IT_CACHE_DIR" || return 1
    curl -sSfL -m "$GEOIP_DOWNLOAD_TIMEOUT" -o "$file.part" "$MIHOMO_GEOIP_URL" || { rm -f "$file.part"; return 1; }
    mv "$file.part" "$file" || return 1
  fi
  printf '%s' "$file"
}

# mihomo_test <配置文件> <日志文件> <工作目录>：在容器里 mihomo -t 检查配置；工作目录由调用方清理
mihomo_test() {
  local cfg="$1" log="$2" home="$3" geoip
  geoip="$(mihomo_geoip)" || { echo "下载 GeoIP 库失败：$MIHOMO_GEOIP_URL" >"$log"; return 1; }
  mkdir -p "$home" && cp "$geoip" "$home/$MIHOMO_GEOIP_FILE" || return 1
  docker run --rm -v "$(dirname "$cfg"):/cfg:ro" -v "$home:/hub-home" "$MIHOMO_IMAGE" \
    -t -f "/cfg/$(basename "$cfg")" -d /hub-home >"$log" 2>&1
}

# assert_http <url> <期望状态码> [期望 body 子串]
assert_http() {
  local url="$1" want="$2" sub="${3:-}"
  local body code
  body="$(curl -s -o /tmp/it-body -w '%{http_code}' "$url")"; code="$body"; body="$(cat /tmp/it-body)"
  [ "$code" = "$want" ] || { echo "assert_http: $url 状态 $code != $want"; return 1; }
  [ -z "$sub" ] || grep -q -- "$sub" /tmp/it-body || { echo "assert_http: $url 响应缺 '$sub': $body"; return 1; }
}

# ---- 管理员分级加固（docs/03 §4 – §4.4，Ruling-95 – Ruling-100）：超管强制两步验证、高危接口二次验证 ----
# 超管登录后必须先绑定 TOTP 才能调业务接口（R10），高危接口要求会话在 auth.reauthWindow 内验证过（R12）。
# 下面的辅助让各用例照旧“建超管 → 登录 → 调接口”：登录时自动绑定或带验证码，配置里把验证有效期拉长。
IT_HUB_BASE="${IT_HUB_BASE:-http://127.0.0.1:8080}"
# 联调配置里的二次验证有效期：要比最长的用例（IT-14 约 10 分钟）长，免得中途过期；IT-17 测过期时另配短窗口
IT_REAUTH_WINDOW="${IT_REAUTH_WINDOW:-2h}"
IT_HUB_CONFIG_SRC="$REPO_ROOT/v2-hub-server/etc/hub-server.yaml"
TOTP_PERIOD_SECONDS=30
TOTP_STEP_MARGIN_SECONDS=1
# 每个账号的 Base32 密钥与最近用过的步长记在这个临时目录里；各用例的 EXIT trap 都会调 stop_all 删掉它
IT_TOTP_DIR="$(mktemp -d)"

# it_hub_config <输出文件> [sed 参数...]：从 etc/hub-server.yaml 派生联调配置，二次验证有效期改为 $IT_REAUTH_WINDOW，
# 其余改动由调用方以 sed 的 -e 参数追加（与原先各用例的 sed 写法相同）；改不到有效期即失败
it_hub_config() {
  local out="$1"; shift
  sed -e "s/^  reauthWindow: .*/  reauthWindow: $IT_REAUTH_WINDOW/" "$@" "$IT_HUB_CONFIG_SRC" > "$out" || return 1
  grep -q "^  reauthWindow: $IT_REAUTH_WINDOW\$" "$out" || { echo "it_hub_config: 没有改到 auth.reauthWindow" >&2; return 1; }
}

# wait_port_free <端口> <秒>：等端口不再被监听（换配置重启前确认旧进程已退出）
wait_port_free() {
  local port="$1" n="${2:-30}"
  for ((i=0; i<n; i++)); do
    (echo >/dev/tcp/127.0.0.1/"$port") 2>/dev/null || return 0
    sleep 1
  done
  return 1
}

# it_admin_cli <子命令> <用户名> [标准输入的一行]：经 make 调 hub 的管理员命令行（admin-create / admin-owner-set /
# admin-totp-reset / admin-password-reset，docs/03 §4.3），写法同各用例的 admin-create；口令只经标准输入传入。
# 输出写 /tmp/it-admin-cli.log
it_admin_cli() {
  local cmd="$1" user="$2" input="${3:-}"
  printf '%s\n' "$input" | (cd "$REPO_ROOT/v2-hub-server" && make -s "$cmd" USERNAME="$user") >/tmp/it-admin-cli.log 2>&1 \
    || { echo "it_admin_cli: $cmd $user 失败，见 /tmp/it-admin-cli.log" >&2; return 1; }
}

# it_totp_code <Base32 密钥> <步长>：RFC 6238 验证码（HMAC-SHA1、30 秒、6 位），python3 标准库实现
it_totp_code() {
  python3 -I -c '
import base64, hashlib, hmac, struct, sys
b32text = sys.argv[1].strip().upper()
key = base64.b32decode(b32text + "=" * (-len(b32text) % 8))
digest = hmac.new(key, struct.pack(">Q", int(sys.argv[2])), hashlib.sha1).digest()
offset = digest[-1] & 0x0F
value = struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF
print("%06d" % (value % 1000000))
' "$1" "$2"
}

it_totp_now_step() { echo $(( $(date +%s) / TOTP_PERIOD_SECONDS )); }

_it_totp_file() { printf '%s/%s.%s' "$IT_TOTP_DIR" "$1" "$2"; }  # _it_totp_file <用户名> <key|step>

# it_totp_forget <用户名>：账号的两步验证被清除后（管理员清除或 admin-totp-reset）丢掉记下的密钥
it_totp_forget() { rm -f "$(_it_totp_file "$1" key)" "$(_it_totp_file "$1" step)"; }

it_totp_has_key() { [ -s "$(_it_totp_file "$1" key)" ]; }

# it_totp_key <用户名>：打印记下的 Base32 密钥
it_totp_key() { cat "$(_it_totp_file "$1" key)"; }

# it_totp_next_code <用户名>：给这个账号取下一个可用的验证码并记下所用步长。
# 服务端防重放（docs/03 §4.3）：同一或更早步长的验证码不再接受，所以同一 30 秒内不能用两次。
# 服务端允许前后各 1 个步长的偏差，这里取“比上次大、且不超过当前 + 1”的最小步长：连着用两次不用等，
# 再用就等到下一个步长开始（多等 1 秒余量）。不取“当前 - 1”，免得请求跨过步长边界后落到窗外。
it_totp_next_code() {
  local user="$1" key last now step wake
  key="$(it_totp_key "$user" 2>/dev/null)"
  [ -n "$key" ] || { echo "it_totp_next_code: 没有 $user 的密钥" >&2; return 1; }
  last="$(cat "$(_it_totp_file "$user" step)" 2>/dev/null || echo 0)"
  now="$(it_totp_now_step)"
  step=$((last + 1))
  [ "$step" -ge "$now" ] || step="$now"
  if [ "$step" -gt $((now + 1)) ]; then
    wake=$(( (step - 1) * TOTP_PERIOD_SECONDS + TOTP_STEP_MARGIN_SECONDS - $(date +%s) ))
    [ "$wake" -le 0 ] || sleep "$wake"
  fi
  printf '%s' "$step" > "$(_it_totp_file "$user" step)"
  it_totp_code "$key" "$step"
}

# _it_admin_post <jar> <csrf> <path> <json>：打印“状态码 响应体”，供下面几个辅助解析
_it_admin_post() {
  local jar="$1" csrf="$2" path="$3" body="$4" out code
  out="$(mktemp)"
  local args=(-s -o "$out" -w '%{http_code}' -b "$jar" -c "$jar" -X POST -H 'Content-Type: application/json' --data "$body")
  [ -z "$csrf" ] || args+=(-H "X-CSRF-Token: $csrf")
  code="$(curl "${args[@]}" "$IT_HUB_BASE$path")"
  printf '%s %s' "$code" "$(cat "$out")"
  rm -f "$out"
}

# it_admin_totp_bind <jar> <csrf> <用户名>：给已登录的会话开启两步验证（setup → 算验证码 → enable），记下密钥与步长
it_admin_totp_bind() {
  local jar="$1" csrf="$2" user="$3" out key code
  out="$(_it_admin_post "$jar" "$csrf" /api/admin/auth/totp/setup '{}')"
  case "$out" in "200 "*) ;; *) echo "it_admin_totp_bind: $user setup 失败：$out" >&2; return 1 ;; esac
  key="$(printf '%s' "${out#* }" | jq -r '.data.secret // empty')"
  [ -n "$key" ] || { echo "it_admin_totp_bind: $user setup 没有返回密钥" >&2; return 1; }
  it_totp_forget "$user"
  printf '%s' "$key" > "$(_it_totp_file "$user" key)"
  code="$(it_totp_next_code "$user")" || return 1
  out="$(_it_admin_post "$jar" "$csrf" /api/admin/auth/totp/enable "$(jq -nc --arg c "$code" '{code:$c}')")"
  case "$out" in "200 "*) ;; *) echo "it_admin_totp_bind: $user enable 失败：$out" >&2; return 1 ;; esac
}

# it_admin_login <jar> <用户名> <密码>：管理员登录，成功时打印 CSRF token。
# 已记下密钥的账号带验证码登录；登录后若是还没开两步验证的超管（totpSetupRequired），当场绑定（R10）。
# 登录成功即视为刚做过二次验证（docs/03 §4.4），在 auth.reauthWindow 内调高危接口不用再验。
it_admin_login() {
  local jar="$1" user="$2" pw="$3" body out csrf code
  if it_totp_has_key "$user"; then
    code="$(it_totp_next_code "$user")" || return 1
    body="$(jq -nc --arg u "$user" --arg p "$pw" --arg c "$code" '{username:$u,password:$p,totpCode:$c}')"
  else
    body="$(jq -nc --arg u "$user" --arg p "$pw" '{username:$u,password:$p}')"
  fi
  out="$(_it_admin_post "$jar" "" /api/admin/auth/login "$body")"
  case "$out" in "200 "*) ;; *) echo "it_admin_login: $user 登录失败：$out" >&2; return 1 ;; esac
  csrf="$(printf '%s' "${out#* }" | jq -r '.data.csrfToken // empty')"
  [ -n "$csrf" ] || { echo "it_admin_login: $user 登录响应缺 csrfToken" >&2; return 1; }
  if [ "$(printf '%s' "${out#* }" | jq -r '.data.totpSetupRequired')" = "true" ]; then
    it_admin_totp_bind "$jar" "$csrf" "$user" || return 1
  fi
  printf '%s' "$csrf"
}

# it_admin_reauth <jar> <csrf> <用户名> [密码]：二次验证（POST /api/admin/auth/reauth）。
# 已开两步验证的账号提交下一个可用的验证码（同样防重放），否则提交密码。打印“状态码 响应体”，200 时返回 0
it_admin_reauth() {
  local jar="$1" csrf="$2" user="$3" pw="${4:-}" body out code
  if it_totp_has_key "$user"; then
    code="$(it_totp_next_code "$user")" || return 1
    body="$(jq -nc --arg c "$code" '{totpCode:$c}')"
  else
    body="$(jq -nc --arg p "$pw" '{password:$p}')"
  fi
  out="$(_it_admin_post "$jar" "$csrf" /api/admin/auth/reauth "$body")"
  printf '%s' "$out"
  case "$out" in "200 "*) return 0 ;; *) return 1 ;; esac
}
