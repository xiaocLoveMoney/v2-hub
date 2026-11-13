# v2-hub-contract · 契约目录

跨模块**唯一**接口真相。这里只放契约文本、建表 SQL、部署编排与黑盒联调脚本，
**不放业务实现与生成物**（API client 等生成物由各模块自己生成并入库）。

| 文件 | 方向 | 内容 |
|---|---|---|
| `hub-api.yaml` | hub-server → 两个前端 | OpenAPI 3.1：管理端 `/api/admin`、客户端 `/api/user`、订阅 `/sub` |
| `deploy/init.sql` | hub-server dao 层 | PostgreSQL 17 建表（与 `migrations/` 的最新一份保持一致） |
| `deploy/migrations/` | hub-server | 增量 SQL，幂等，手工执行，同步回 init.sql |
| `upstream/3x-ui/openapi-v3.11.0.json` | 3x-ui → hub-server | 上游面板 API 快照，Provider(xui) 契约测试基准（Ruling-7） |
| `deploy/docker-compose.yml` | 开发 | PostgreSQL、MinIO；`--profile xui` 另起两台 3x-ui |
| `deploy/compose.deploy.yml` + `deploy/caddy/Caddyfile` | 生产 | hub-server + Caddy + PostgreSQL + MinIO |
| `deploy/publish_web.sh` | 前端 → MinIO | 发布前端构建产物到 `web-admin` / `web-user` 桶 |
| `deploy/it/` | 黑盒联调 | `run_all.sh` 汇总 IT-N 用例 |

```bash
cd deploy
bash init_env.sh                                   # 生成 .env（随机口令）
docker compose -f docker-compose.yml up -d --wait  # PostgreSQL :15432、MinIO :19000/:19001
bash reset_db.sh                                   # 重置库到 init.sql
cd it && bash run_all.sh                           # 联调回归（会重置库）
```

- 变更纪律：提 PR；破坏性变更在 PR 描述列受影响目录并在同一 PR 改引用方（Ruling-3）。
- 版本：git tag `vX.Y[.Z]`，当前 `v0.8`（M7：服务端链式；hub-api 0.4.2，migrations 0011）；main 上已是 M8 的 hub-api 0.4.3 与 migrations 0012。
- SQL 由使用者手工执行（`reset_db.sh` 重置、`migrations/` 增量），模块不自动建表。
