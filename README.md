# v2-hub

[![deploy](https://github.com/xiaocLoveMoney/v2-hub/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/xiaocLoveMoney/v2-hub/actions/workflows/deploy.yml)
[![Docker Pulls](https://img.shields.io/docker/pulls/xiaoclux/v2-hub-server)](https://hub.docker.com/r/xiaoclux/v2-hub-server)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**多服务器 3x-ui 节点聚合与订阅分发平台。** 把分散在多台服务器上的 [3x-ui](https://github.com/MHSanaei/3x-ui) 面板聚合到一处，
按套餐与权限把节点派发给用户，对外只给一个订阅地址；支持客户端 / 服务端两种链式代理、SSH 托管面板、商店与钱包。
v2-hub 只做控制面，**不转发任何代理流量**。

## 功能

| 方面 | 内容 |
|---|---|
| 面板聚合 | 接入多台 3x-ui（面板地址 + API 令牌），定时同步与对账；入站增删改查（表单与 3x-ui 同构）、一键部署 7 种常用入站预设；节点目录、节点组、服务器状态卡片（CPU / 内存 / 流量额度 / 三网延迟） |
| 用户与订阅 | 套餐（到期、流量、周期重置）、超额 / 到期自动停用；统一订阅 `/{随机前缀}/{令牌}`，输出 Base64 / Mihomo / sing-box / Xray JSON；重置订阅即轮换连接凭据 |
| 链式代理 | 客户端链式（订阅里带两跳）与服务端链式（平台在入口面板下发出站与路由）；落地可选平台节点、平台出口或用户自有出口（SOCKS5 / HTTP / 分享链接），带出口延迟测试 |
| SSH 托管 | 保存 SSH 凭据后由平台安装或接管 3x-ui、升级、重启、签证书（自签，或经 Cloudflare DNS 用 acme.sh 申请 Let's Encrypt），只有固定动作、没有任意命令 |
| 运营 | 自助注册 / 邀请码、邮箱验证、公告、教程、工单、签到；余额、订单、优惠码、充值码、自动续费、邀请返利、收支统计；掉线告警（邮件 / Telegram）、大陆连通检测 |
| 管理 | RBAC 角色与权限码、两步验证、高危操作二次验证、审计日志、二级代理；用户端中英双语 |

## 架构

```
3x-ui A ┐                          ┌──────── 一台独立服务器（docker compose）────────┐
3x-ui B ┼── HTTPS + Bearer API ────┤  Caddy :443 ─┬─ /api、订阅地址 → hub-server :8080  │
3x-ui C ┘  （hub-server 主动轮询    │              ├─ /admin/        → MinIO 桶 web-admin │
            与下发；SSH 托管可选）  │              └─ 其它           → MinIO 桶 web-user  │
                                   │  hub-server ── PostgreSQL；MinIO 另存上传图片       │
                                   └────────────────────────────────────────────────────┘
代理客户端 ── 拉订阅 → https://你的域名/{前缀}/{令牌}；代理流量直连各 3x-ui 节点，不经过 v2-hub
```

## 容器部署

### 镜像

两个镜像，均支持 `linux/amd64` 与 `linux/arm64`，Docker Hub 与 GHCR 内容相同：

| 镜像 | Docker Hub | GHCR | 说明 |
|---|---|---|---|
| v2-hub-server | `xiaoclux/v2-hub-server` | `ghcr.io/xiaoclovemoney/v2-hub-server` | 后端（distroless、非 root） |
| v2-hub-init | `xiaoclux/v2-hub-init` | `ghcr.io/xiaoclovemoney/v2-hub-init` | 一次性初始化：数据库迁移、上传桶、把管理端与用户端前端发布到 MinIO，完成即退出 |

| 标签 | 含义 |
|---|---|
| `latest` | 最新发布版本（仓库打 `vX.Y` tag 时更新） |
| `vX.Y` | 固定版本，正式环境建议固定 |
| `edge` | `main` 分支最新一次通过检查的构建 |
| `sha-<提交号>` | 某个提交的构建 |

两个镜像必须用**同一个标签**（编排里已经这样写）。PostgreSQL、MinIO、Caddy 用官方 / 社区镜像，编排里已固定版本。

### 准备

- 一台 Linux 服务器，装好 Docker 与 Compose 插件（`docker compose version` ≥ 2.17）
- 一个域名，A / AAAA 记录指向这台服务器；防火墙放行 80、443（TCP 与 UDP）。Caddy 会自动申请 HTTPS 证书
- 3x-ui 面板放在别的服务器上（也可以和 v2-hub 同机，或之后由 v2-hub 经 SSH 安装）

### 1. 下载编排文件

```bash
sudo mkdir -p /opt/v2-hub/caddy && cd /opt/v2-hub
REF=main   # 也可换成版本 tag，如 v0.24，与下面的镜像标签对应
BASE=https://raw.githubusercontent.com/xiaocLoveMoney/v2-hub/$REF/v2-hub-contract/deploy
sudo curl -fsSLo compose.yaml       $BASE/compose.deploy.yml
sudo curl -fsSLo caddy/Caddyfile    $BASE/caddy/Caddyfile
sudo curl -fsSLo deploy.env.example $BASE/deploy.env.example
sudo curl -fsSLo init_env.sh        $BASE/init_env.sh
```

### 2. 生成 `.env`

```bash
sudo bash init_env.sh deploy.env.example .env   # 数据库、MinIO、加密主密钥等填入随机值，文件权限 600
sudo vi .env                                    # 至少把 HUB_DOMAIN 改成你的域名
```

| 变量 | 必填 | 说明 |
|---|---|---|
| `HUB_DOMAIN` | 是 | 站点域名。用户端在根路径，管理端在 `/admin/` |
| `HUB_ENCRYPTION_KEY` | 是 | 加密主密钥（已自动生成）。两步验证、面板令牌、SSH 凭据等都用它加密存库，**丢了无法解密，务必另行备份** |
| `POSTGRES_*` / `MINIO_*` / `HUB_UPLOAD_*` | 是 | 已自动生成，一般不用改 |
| `HUB_IMAGE_REGISTRY` | 否 | 默认 `docker.io/xiaoclux`，可换 `ghcr.io/xiaoclovemoney` |
| `HUB_IMAGE_TAG` | 否 | 默认 `latest`，建议固定为版本号 |
| `HUB_SMTP_*` | 否 | 发信（注册验证码、找回密码、到期提醒）；不填则依赖邮件的功能关闭 |
| `HUB_TELEGRAM_BOT_TOKEN` | 否 | 服务器掉线告警 |
| `HUB_GLOBALPING_TOKEN` | 否 | 大陆连通检测 / 三网延迟用的 Globalping 令牌，不填按免费额度 |

### 3. 启动

```bash
sudo docker compose up -d
sudo docker compose ps -a       # init 显示 Exited (0)，其余 running 即正常
sudo docker compose logs init   # 首次启动可看迁移与前端发布过程
```

启动顺序：PostgreSQL、MinIO → `init` 完成迁移与前端发布 → hub-server → Caddy。

### 4. 创建超级管理员

```bash
sudo docker compose run --rm --no-deps -it --entrypoint /app/hub hub-server \
  admin-create -f /app/etc/hub-server.yaml -username admin
```

密码在终端里无回显输入。库里还没有所有者时，这个超管即“所有者”；首次登录后须先开启两步验证。

### 5. 开始使用

- 管理端 `https://你的域名/admin/`：在“服务器”里接入 3x-ui 面板（面板根地址 + API 令牌），或在“SSH 托管”里让平台安装 / 接管面板；然后建节点组、套餐、用户
- 用户端 `https://你的域名/`：用户登录后复制订阅地址，导入 Mihomo / sing-box / v2rayN 等客户端
- 用 Cloudflare 代理域名时，SSL/TLS 模式选“完全（严格）”

### 升级

```bash
cd /opt/v2-hub
sudo vi .env                         # 固定了版本号的，把 HUB_IMAGE_TAG 改成新版本
sudo docker compose pull && sudo docker compose up -d
```

`init` 每次启动都会执行尚未执行的数据库迁移并重新发布前端，无需手工操作。新版本的编排文件有变化时（见发布说明），按步骤 1 重新下载 `compose.yaml` 与 `Caddyfile`。

### 备份

```bash
cd /opt/v2-hub
sudo docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' | gzip > v2hub-$(date +%F).sql.gz
```

另外务必保存好 `.env`（尤其是 `HUB_ENCRYPTION_KEY`）。公告 / 教程里上传的图片在数据卷 `v2-hub_minio-data` 的 `uploads` 桶里。

### 管理员找回

都在服务器上执行，前缀同“创建超级管理员”（`docker compose run --rm --no-deps -it --entrypoint /app/hub hub-server`）：

| 子命令 | 作用 |
|---|---|
| `admin-password-reset -f /app/etc/hub-server.yaml -username <名称>` | 重设密码并吊销其全部会话 |
| `admin-totp-reset -f /app/etc/hub-server.yaml -username <名称>` | 清除两步验证（遗失验证器时） |
| `admin-owner-set -f /app/etc/hub-server.yaml -username <名称>` | 指定或转移所有者 |

### 自定义配置

hub-server 的非敏感配置（轮询间隔、会话时长、频控等）在镜像内的 `/app/etc/hub-server.yaml`。
需要改时从 [`v2-hub-server/etc/hub-server.yaml`](v2-hub-server/etc/hub-server.yaml) 复制一份，在 `compose.yaml` 的 `hub-server` 下挂载：

```yaml
    volumes:
      - ./hub-server.yaml:/app/etc/hub-server.yaml:ro
```

凭据一律只走 `.env` / 环境变量，不写进配置文件。

### 从源码构建镜像

```bash
docker build -t v2-hub-server v2-hub-server
docker build -f v2-hub-contract/deploy/init/Dockerfile -t v2-hub-init .   # 构建上下文是仓库根目录
```

## 开发

仓库是 monorepo，各模块目录下的 `contract` 是指向 `../v2-hub-contract` 的符号链接。

| 目录 | 技术栈 | 说明 |
|------|--------|------|
| [v2-hub-contract](v2-hub-contract) | OpenAPI 3.1 / PostgreSQL SQL / compose | 契约目录：跨模块唯一接口真相、部署编排、黑盒联调 |
| [v2-hub-server](v2-hub-server) | Go 1.27 | 后端：API、订阅端点、面板聚合与下发 |
| [v2-hub-admin-web](v2-hub-admin-web) | React 19 + TS + Vite | 管理端 |
| [v2-hub-user-web](v2-hub-user-web) | React 19 + TS + Vite | 用户端 |

```bash
# 中间件：PostgreSQL :15432、MinIO :19000 / :19001（加 --profile xui 另起两台 3x-ui :12053 / :12054）
cd v2-hub-contract/deploy && bash init_env.sh && docker compose -f docker-compose.yml up -d --wait \
  && bash reset_db.sh && bash init_uploads.sh && cd ../..
cd v2-hub-server && make run        # :8080，另开终端
cd v2-hub-admin-web && make run     # :5173，另开终端
cd v2-hub-user-web && make run      # :5174，另开终端
```

- 每个模块统一 `make run | make test | make build | make lint`；联调回归 `cd v2-hub-contract/deploy/it && bash run_all.sh`（会重置开发库）
- 改代码前先读 [`CLAUDE.md`](CLAUDE.md) 与 [`docs/`](docs/README.md)（业务语义以 `docs/03` 为准，决定记在 [`docs/决议台账.md`](docs/决议台账.md)）；认领模块先读该目录的《任务与边界.md》
- 一条分支只改一个模块目录；契约变更只在 `v2-hub-contract/`；`main` 只经 PR 合入（merge commit）
- 提交格式 `type(scope): 中文摘要 —— 原因`；分支名 `<type>/<模块>-<主题>`

## 许可证

[MIT](LICENSE)
