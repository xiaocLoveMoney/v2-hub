# 05 · 详细设计 · hub-admin-web

> 负责人：@xiaoclux。职责：管理门户（登录 + 总览大盘）与管理端。契约：`hub-api.yaml` 的 `/api/admin`。

## 1. 目录结构

```
v2-hub-admin-web/
├── src/
│   ├── api/            # health.ts（已有）；generated/ 放由 hub-api.yaml 生成的 client（M0）
│   ├── App.tsx         # 根组件（脚手架阶段只做探活）
│   ├── ErrorBoundary.tsx
│   └── main.tsx
├── vite.config.ts      # 开发端口 5173，/api 代理到 8080
└── vitest.config.ts
```

## 2. 模块划分与依赖方向

`pages → components → api`；页面不直接 `fetch`，统一经 `src/api`。路由、布局、状态库在 M0 登录任务中选定并登记决议。

## 3. 关键不变式

1. 菜单与按钮按后端返回的权限码显示；隐藏只是体验，鉴权以后端为准。
2. 不在 localStorage 存会话或凭据；会话靠 HttpOnly Cookie。
3. 面板 token 等凭据只写不读：表单可提交，详情页只显示“已设置”。
4. 删除入站前展示受影响用户数并二次确认。

## 4. 与其它模块的握手

| 对方 | 方向 | 协议 | 契约文件 |
|---|---|---|---|
| hub-server | 主调 | HTTP `/api/admin`（同域） | `hub-api.yaml` |

## 5. 配置与启动

`make run`（5173）；生产 `make build` 后 `publish_web.sh dist web-admin`。

## 6. 测试策略

Vitest + Testing Library；`fetch` 用 `vi.stubGlobal` 模拟。

## 7. 接口待补 / 契约缺口

- 见《任务与边界》。
