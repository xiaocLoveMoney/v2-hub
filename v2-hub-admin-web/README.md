# v2-hub-admin-web

管理侧前端：管理门户与管理端。技术栈 React 19 + TypeScript + Vite 8（Node 24），开发端口 5173。

```bash
make run    # Vite 开发服务器，/api 与 /healthz 代理到 127.0.0.1:8080
make test   # 类型检查 + Vitest
make build  # 产出 dist/
make lint   # oxlint
npm run gen:api   # 契约 hub-api.yaml 变更后重新生成 src/api/generated/schema.d.ts（入库，Ruling-1）
```

发布：生产构建的 base 为 `/admin/`（Ruling-61），`main` 检查通过后由部署脚本构建并发布到 MinIO 桶 `web-admin`（Ruling-6、Ruling-62）。

契约：`contract/` → `../v2-hub-contract`。任务与边界见《任务与边界.md》；设计见 `docs/05-详细设计-hub-admin-web.md`。
