# 06 · 详细设计 · hub-user-web

> 负责人：@xiaoclux。职责：客户门户与客户后台。契约：`hub-api.yaml` 的 `/api/user`。

## 1. 目录结构

与 hub-admin-web 相同（`src/api`、`App.tsx`、`ErrorBoundary.tsx`、`main.tsx`），开发端口 5174。

## 2. 模块划分与依赖方向

`pages → components → api`；页面不直接 `fetch`。不 import admin-web 的代码。

## 3. 关键不变式

1. 菜单按套餐功能开关显示（链式代理、自有落地节点）；后端仍做最终鉴权与归属校验。
2. 订阅地址与分享链接属于凭据：默认打码，用户主动点击才显示 / 复制；不进埋点与日志。
3. 订阅内容只由 hub-server 渲染，前端只展示地址与二维码。

## 4. 与其它模块的握手

| 对方 | 方向 | 协议 | 契约文件 |
|---|---|---|---|
| hub-server | 主调 | HTTP `/api/user`（同域）；订阅 `/sub/*` 由代理客户端直接拉取 | `hub-api.yaml` |

## 5. 配置与启动

`make run`（5174）；生产 `make build` 后 `publish_web.sh dist web-user`。

## 6. 测试策略

Vitest + Testing Library。

## 7. 接口待补 / 契约缺口

- 见《任务与边界》。
