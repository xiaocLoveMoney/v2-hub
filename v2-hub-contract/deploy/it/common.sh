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

stop_all() {
  for p in "${IT_PIDS[@]:-}"; do
    [ -n "$p" ] || continue
    kill -- "-$p" 2>/dev/null || kill "$p" 2>/dev/null || true   # 进程可能已自行退出
  done
  IT_PIDS=()
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
