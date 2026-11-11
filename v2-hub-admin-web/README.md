# v2-hub-admin-web

管理侧前端：管理门户与管理端。技术栈 React 19 + TypeScript + Vite 8（Node 24），开发端口 5173。

```bash
make run    # Vite 开发服务器，/api 与 /healthz 代理到 127.0.0.1:8080
make test   # 类型检查 + Vitest
make build  # 产出 dist/
make lint   # oxlint
```

发布：`make build` 后执行 `bash ../v2-hub-contract/deploy/publish_web.sh dist web-admin`，产物进 MinIO（Ruling-6）。

契约：`contract/` → `../v2-hub-contract`。任务与边界见《任务与边界.md》；设计见 `docs/05-详细设计-hub-admin-web.md`。
