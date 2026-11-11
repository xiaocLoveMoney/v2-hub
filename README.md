# v2-hub · 多服务器节点聚合与订阅分发平台

把多台服务器上的 3x-ui 面板聚合到一处，按权限把节点派发给用户，对外只给一个订阅地址，并支持用户自配链式代理。
v2-hub 部署在独立服务器上，只做控制面，**不转发代理流量**。

> 改代码前先读 `CLAUDE.md` 与 `docs/`（索引 `docs/README.md`）；认领模块先读该目录的《任务与边界.md》。

## 目录与分工

| 目录 | 技术栈 | 负责人 | 说明 |
|------|--------|--------|------|
| v2-hub-contract | OpenAPI 3.1 / PostgreSQL SQL / compose | 全体 | 契约目录：跨模块唯一接口真相 |
| v2-hub-server | Go 1.27 | @xiaoclux | 后端：API、订阅端点、面板聚合与下发 |
| v2-hub-admin-web | React 19 + TS + Vite | @xiaoclux | 管理门户 + 管理端 |
| v2-hub-user-web | React 19 + TS + Vite | @xiaoclux | 客户门户 + 客户后台 |

各模块目录下的 `contract` 是指向 `../v2-hub-contract` 的符号链接。

## 架构一眼看

```
3x-ui A ┐                        ┌─ admin 域名 ─ Caddy ─┬─ MinIO 桶 web-admin（静态）
3x-ui B ┼─ Bearer API ─ hub-server┤                      └─ /api → hub-server
3x-ui C ┘  (轮询 + 下发)    │      └─ user 域名 ── Caddy ─┬─ MinIO 桶 web-user（静态）
                       PostgreSQL                         └─ /api、/sub → hub-server
```

- 面板是入站配置的事实来源；hub-server 存缓存副本并对账（docs/02）。
- 业务层只依赖面板 Provider 接口，3x-ui 只是其中一个实现（Ruling-8）。

## 端口一览

| 端口 | 服务 |
|------|------|
| 8080 | hub-server |
| 5173 | v2-hub-admin-web 开发服务器 |
| 5174 | v2-hub-user-web 开发服务器 |
| 15432 | PostgreSQL（开发，宿主机） |
| 19000 / 19001 | MinIO API / 控制台（开发，宿主机） |
| 12053 / 12054 | 联调用 3x-ui 面板 A / B（`--profile xui`） |

## 本地从零到跑通

```bash
cd v2-hub-contract/deploy && bash init_env.sh && docker compose -f docker-compose.yml up -d --wait && bash reset_db.sh && cd ../..
cd v2-hub-server && make run          # 另开终端
cd v2-hub-admin-web && make run       # 另开终端
cd v2-hub-user-web && make run        # 另开终端
```

各端验证：每模块 `make test`；联调 `cd v2-hub-contract/deploy/it && bash run_all.sh`（会重置数据库）。

## 认领模块的三步

```bash
git worktree add ../v2-hub-<模块>-wt -b feat/<模块>-<主题> main
cat v2-hub-<模块>/任务与边界.md      # 再读 docs/03 与自己那篇
cd v2-hub-<模块> && make run && make test
```

worktree 里只 `git add` 自己模块的路径；小步提交；push 后开 PR（模板自动带出），`Closes #issue`。

## 纪律

1. `main` 只经 PR 合入（merge commit，不 squash）；任意分支跑测试。
2. 契约变更只在 `v2-hub-contract/`；破坏性变更同一 PR 改引用方。
3. 业务语义以 docs/03 为准，先改文档再改代码。
4. 配置入库、凭据不入库（`deploy/init_env.sh` 生成本机 `.env`）。
