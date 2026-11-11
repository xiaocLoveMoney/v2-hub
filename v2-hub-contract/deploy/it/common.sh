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
  ( cd "$REPO_ROOT/$dir" && env "$@" make run >"/tmp/it-$dir.log" 2>&1 ) &
  IT_PIDS+=($!)
  set +m
  wait_port "$port" 30 || { echo "start_module: $dir 在 $port 端口 30s 未就绪, 日志 /tmp/it-$dir.log"; return 1; }
}

wait_port() {  # wait_port <端口> <秒>
  local port="$1" n="${2:-30}"
  for ((i=0; i<n; i++)); do
    (echo >/dev/tcp/127.0.0.1/"$port") 2>/dev/null && return 0
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

# assert_http <url> <期望状态码> [期望 body 子串]
assert_http() {
  local url="$1" want="$2" sub="${3:-}"
  local body code
  body="$(curl -s -o /tmp/it-body -w '%{http_code}' "$url")"; code="$body"; body="$(cat /tmp/it-body)"
  [ "$code" = "$want" ] || { echo "assert_http: $url 状态 $code != $want"; return 1; }
  [ -z "$sub" ] || grep -q -- "$sub" /tmp/it-body || { echo "assert_http: $url 响应缺 '$sub': $body"; return 1; }
}
