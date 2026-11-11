# CLAUDE.md

## 这是什么

多服务器 3x-ui 节点聚合与订阅分发平台：聚合各面板的入站，按权限派发给用户，提供统一订阅与链式代理。
仓库是 **monorepo**（GitHub，账号 xiaoclux），顶层目录按模块平铺，各模块目录下的 `contract` 是指向
`../v2-hub-contract` 的符号链接。

| 目录 | 技术栈 | 负责人 | 说明 |
|------|--------|--------|------|
| v2-hub-contract | OpenAPI / SQL / compose | 全体 | 契约目录：`hub-api.yaml`、`deploy/init.sql`、compose、IT 联调、3x-ui 上游快照 |
| v2-hub-server | Go 1.27 | @xiaoclux | 后端 |
| v2-hub-admin-web | React + TS + Vite | @xiaoclux | 管理门户 + 管理端 |
| v2-hub-user-web | React + TS + Vite | @xiaoclux | 客户门户 + 客户后台 |

## 常用命令

```bash
# 中间件（PostgreSQL :15432、MinIO :19000/:19001；加 --profile xui 另起两台 3x-ui）
cd v2-hub-contract/deploy && bash init_env.sh && docker compose -f docker-compose.yml up -d --wait && bash reset_db.sh
# 每个模块统一四个目标
cd v2-hub-<module> && make run | make test | make build | make lint
# 联调回归（脚本自起自停各模块进程，会重置数据库）
cd v2-hub-contract/deploy/it && bash run_all.sh
```

开发用的 PostgreSQL / MinIO 一律用 docker 起，不装本机服务；本机 5432/9000/9001 被其它项目占用，所以宿主机端口错开。

## 架构（跨模块大图）

- v2-hub 只做控制面，不转发代理流量（Ruling-4）。
- 面板是入站配置的事实来源；hub-server 缓存副本，定时同步并对账用户 client（docs/02 §3）。
- 业务层只依赖 `manager/panel` 的 Provider 接口与中立模型，3x-ui 是 `manager/panel/xui` 一个实现（Ruling-8）。
- 前端静态文件放 MinIO，Caddy 同域反代 `/api`、`/sub`（Ruling-5、Ruling-6）。

契约（全部在 v2-hub-contract/）：`hub-api.yaml`（server → 前端）、`deploy/init.sql`（server 的表结构）、
`upstream/3x-ui/openapi-v3.11.0.json`（3x-ui → server 的上游基准）。契约版本用 git tag `vX.Y`，当前未打 tag。

## 跨目录纪律

- 契约变更只发生在 `v2-hub-contract/`；破坏性变更在同一 PR 同步改引用方。
- 一条分支只碰一个模块目录；要改别的目录 = 契约缺口，记进《任务与边界》并开 issue。
- 业务语义以 docs/03 为准，**改动先改文档再改代码**；代码注释引用 `Ruling-N` / `T-N` 编号，不复述规则。
- 生成物入库，重生成后 review diff。
- 配置文件入库（`etc/`），凭据只在 `.env` / 环境变量 / GitHub Secrets。
- 提交格式 `type(scope): 中文摘要 —— 原因`，小步提交；合并用 merge commit。

## 开发文档（改代码前先读）

`docs/` 编号篇，索引在 `docs/README.md`。最小阅读集 = 01/02/03 + 自己模块那篇 + 自己目录的《任务与边界.md》。
GitHub 仓库设置见 `docs/github-仓库设置清单.md`。

## 当前完成度

- 脚手架初始化（2026-11-11）：三个模块 `make test` 全绿，hub-server `/healthz` 可跑，前端首页可探活后端；
  `init.sql` 含 M0 管理员与 RBAC 表；`hub-api.yaml` 只有 `/healthz`。里程碑 M0 其余任务未开始。
