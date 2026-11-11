#!/usr/bin/env bash
# IT-1 冒烟：起全部模块；hub-server /healthz 返 200 {"status":"ok"}，两个前端首页返 200。
source "$(dirname "$0")/common.sh"
set -e
start_module v2-hub-server 8080
start_module v2-hub-admin-web 5173
start_module v2-hub-user-web 5174
assert_http http://127.0.0.1:8080/healthz 200 '"status":"ok"'
assert_http http://127.0.0.1:5173/ 200 '<div id="root">'
assert_http http://127.0.0.1:5174/ 200 '<div id="root">'
echo "IT-1 PASS"
