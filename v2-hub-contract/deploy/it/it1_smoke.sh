#!/usr/bin/env bash
# IT-1 冒烟：起全部模块；hub-server /healthz 与 /readyz（含数据库）返 200，两个前端首页返 200。
# 前端开发服务器按 localhost 监听（可能只在 ::1 上），所以用 localhost 访问。
source "$(dirname "$0")/common.sh"
set -e
start_module v2-hub-server 8080
start_module v2-hub-admin-web 5173
start_module v2-hub-user-web 5174
assert_http http://127.0.0.1:8080/healthz 200 '"status":"ok"'
assert_http http://127.0.0.1:8080/readyz 200 '"status":"ok"'
assert_http http://localhost:5173/ 200 '<div id="root">'
assert_http http://localhost:5174/ 200 '<div id="root">'
echo "IT-1 PASS"
