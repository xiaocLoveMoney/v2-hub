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
`upstream/3x-ui/openapi-v3.11.0.json`（3x-ui → server 的上游基准）。契约版本用 git tag `vX.Y`，当前 `v0.3`。

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

- M0 完成（2026-11-12，tag `v0.1`）：管理员登录 / 会话 / CSRF / 登录频控、RBAC（R1–R7）与管理员 / 角色 / 审计日志接口和页面、
  TOTP 两步验证（AES-256-GCM 加密，主密钥 `HUB_ENCRYPTION_KEY`）、`make admin-create`；IT-1、IT-2 通过。
- M1 完成（2026-11-12，tag `v0.2`）：面板 Provider 抽象与 3x-ui 实现、服务器接入 / 探测 / 同步、入站增删改查
  （表单与 3x-ui 同构，Ruling-16）、Host 对外地址、节点目录与节点组、漂移与面板标识复用处理；IT-3 通过。
- M2 完成（2026-11-13，tag `v0.3`）：套餐与用户（额度模型照 3x-ui：到期、首次使用起算、周期流量重置）、
  对账器（每用户在每台面板一个 client，失败重试）、流量汇总、超额 / 到期自动禁用、管理员直接改用量；IT-4 通过。
- 用户端（v2-hub-user-web）仍是脚手架首页；下一步是 M3（订阅与用户端基础）。
- 路线（2026-11-13，Ruling-17 – Ruling-22）：参考 SSPanel-Uim Malio 的功能与界面，自行实现；里程碑为
  M3 订阅与用户端基础 → M4 自助运营 → M5 商店与钱包 → M6 / M7 两种链式 → M8 运营，见 docs/07。
