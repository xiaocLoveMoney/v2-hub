# 05 · 详细设计 · hub-admin-web

> 负责人：@xiaoclux。职责：管理门户（登录 + 总览大盘）与管理端。契约：`hub-api.yaml` 的 `/api/admin`。

## 1. 目录结构

```
v2-hub-admin-web/
├── src/
│   ├── api/            # client.ts（openapi-fetch + CSRF）、generated/（由 hub-api.yaml 生成）
│   ├── auth/           # 会话上下文
│   ├── hooks/          # use-remote、use-open-generation
│   ├── layout/         # AdminLayout
│   ├── pages/          # panel/、users/、system/ 等
│   ├── menu.tsx        # 菜单表（驱动左侧菜单与路由守卫）
│   ├── routes.tsx
│   ├── App.tsx         # ConfigProvider（主题 token）与路由
│   ├── ErrorBoundary.tsx
│   └── main.tsx
├── vite.config.ts      # 开发端口 5173，/api 代理到 8080
└── vitest.config.ts
```

## 2. 模块划分与依赖方向

`pages → components → api`；页面不直接 `fetch`，统一经 `src/api`。UI 用 antd 6，路由 react-router 8，接口类型由契约生成（Ruling-13）。会话放 React Context（`src/auth`），不引入额外状态库；
菜单表 `src/menu.tsx` 同时驱动左侧菜单与路由守卫，增减菜单只改这张表。

界面观感与用户端统一（Ruling-21）：保留 antd 与现有页面，只在三处换肤——`src/App.tsx` 的 `ConfigProvider`
主题 token（主色 `#6777ef`、圆角 3、页面底 `#f4f6f9`、字体 Nunito）；`src/layout/AdminLayout.tsx` 改为 250px
白色侧栏 + 115px 主色色带 + 页头；`src/index.css` 调卡片阴影与徽章。取值与 06 篇 §4 的设计变量一致。

菜单分组：我的（总览、公告、工单）、节点（服务器、节点）、用户（用户、套餐、订阅记录、登录记录）、
交易（商品定价、订单、充值码、优惠码、返利）、系统（管理员、角色与权限、审计日志、系统设置、账号安全）；
各项随 M3–M8 加入，权限码照旧由菜单表驱动。

## 3. 关键不变式

1. 菜单与按钮按后端返回的权限码显示；隐藏只是体验，鉴权以后端为准。
2. 不在 localStorage 存会话或凭据；会话靠 HttpOnly Cookie。
3. 面板 token 等凭据只写不读：表单可提交，详情页只显示“已设置”。
4. 删除入站前展示受影响用户数并二次确认。
5. 换肤不改页面结构与交互；每次换肤改动后现有测试必须全部通过。
6. “切换为用户身份”须有独立权限码并记审计，界面上始终显示返回管理员身份的入口（M5）。

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
