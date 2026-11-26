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
| `deploy/init_uploads.sh` | hub-server → MinIO | 建图片上传桶 `uploads`（匿名只读）与 hub-server 的只写账号，`.env` 缺账号时补上（Ruling-89） |
| `deploy/deploy_prod.sh` | 生产部署 | 自托管 Runner 在 `main` 检查全绿后执行：本地构建、迁移、compose 更新、发布前端、健康检查（Ruling-62） |
| `deploy/it/` | 黑盒联调 | `run_all.sh` 汇总 IT-N 用例 |

```bash
cd deploy
bash init_env.sh                                   # 生成 .env（随机密码）
docker compose -f docker-compose.yml up -d --wait  # PostgreSQL :15432、MinIO :19000/:19001
bash reset_db.sh                                   # 重置库到 init.sql
bash init_uploads.sh                               # 图片上传用的桶与账号（可重复执行）
cd it && bash run_all.sh                           # 联调回归（会重置库）
```

- 变更纪律：提 PR；破坏性变更在 PR 描述列受影响目录并在同一 PR 改引用方（Ruling-3）。
- 版本：git tag `vX.Y[.Z]`，最新 tag `v0.23`（主机域名解析与一键部署入站）；其后未打 tag 的契约现状为 hub-api 0.18.0、migrations 0026、IT-18（管理员分级加固与二级代理，Ruling-95 – Ruling-108）。
- SQL 由使用者手工执行（`reset_db.sh` 重置、`migrations/` 增量），模块不自动建表。
