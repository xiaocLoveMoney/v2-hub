# IT-14、IT-15 用的“托管主机”（IT-15 用 python3 在容器里跑服务器侧探测的模拟端）：Debian + sshd + 按官方目录布局放好的 3x-ui（docs/08，docs/03 §5.22）。
# 容器里没有 systemd，systemctl 是只实现了几个子命令的垫片；真实的 systemd 行为要在真机上人工验证。
FROM debian:12-slim
ARG XUI_TAG=v3.11.0
# 装 openssh-server 时会顺带生成主机密钥；删掉，留给 entrypoint 在每次启动时现生成，否则所有容器共用镜像里那一套
RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-server ca-certificates curl tar openssl procps iproute2 python3-minimal \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/ssh/ssh_host_* \
    && mkdir -p /run/sshd /root/.ssh /etc/x-ui \
    && chmod 700 /root/.ssh
# 程序包与真机安装时下载的是同一份（上游按架构发布）；这里只解包，不跑安装脚本
RUN arch="$(dpkg --print-architecture)" \
    && case "$arch" in amd64|arm64) ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac \
    && curl -fsSL -o /tmp/x-ui.tar.gz "https://github.com/MHSanaei/3x-ui/releases/download/${XUI_TAG}/x-ui-linux-${arch}.tar.gz" \
    && curl -fsSL -o /tmp/x-ui.sha256 "https://github.com/MHSanaei/3x-ui/releases/download/${XUI_TAG}/x-ui-linux-${arch}.tar.gz.sha256" \
    && [ "$(awk 'NR == 1 {print $1}' /tmp/x-ui.sha256)" = "$(sha256sum /tmp/x-ui.tar.gz | awk '{print $1}')" ] \
    && tar -xzf /tmp/x-ui.tar.gz -C /usr/local \
    && rm -f /tmp/x-ui.tar.gz /tmp/x-ui.sha256 \
    && chmod +x /usr/local/x-ui/x-ui /usr/local/x-ui/bin/xray-linux-*
COPY systemctl /usr/local/bin/systemctl
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /usr/local/bin/systemctl /entrypoint.sh
EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
