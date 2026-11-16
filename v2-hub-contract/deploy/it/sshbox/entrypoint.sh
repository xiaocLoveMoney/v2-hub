#!/bin/sh
# IT-14 托管主机的入口：每次启动生成新的 SSH 主机密钥（重建容器 = 机器重装），
# 写入联调脚本给的公钥，把面板端口固定成对外发布的那个，再用循环守着 x-ui。
set -eu
: "${AUTHORIZED_KEY:?需要 AUTHORIZED_KEY（联调脚本生成的公钥）}"
: "${PANEL_PORT:?需要 PANEL_PORT}"
ssh-keygen -A >/dev/null
printf '%s\n' "$AUTHORIZED_KEY" > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
cd /usr/local/x-ui
./x-ui setting -port "$PANEL_PORT" >/dev/null
( while true; do ./x-ui >>/var/log/x-ui.log 2>&1 || true; sleep 1; done ) &
exec /usr/sbin/sshd -D -e -o PermitRootLogin=prohibit-password -o PasswordAuthentication=no
