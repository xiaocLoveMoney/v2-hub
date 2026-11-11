---
paths:
  - "**/pom.xml"
  - "**/build.gradle*"
  - "**/package.json"
  - "**/pyproject.toml"
  - "**/go.mod"
  - "**/Dockerfile"
---

# 工程结构规约

> 应用分层与领域模型规约已拆至 `rules/architecture.md`（常驻加载）——本文件按 paths 只在读构建文件时注入，架构规则放这里在编辑源码时不生效。

## 依赖与版本（二方库规约，推广到全语言包管理）

1. 【强制】版本号 `主.次.修订`：不兼容 API 变更升主版本；向下兼容新功能升次版本；修 bug 升修订号。首个正式版 1.0.0（非 0.0.1）。
2. 【强制】线上应用禁止依赖 SNAPSHOT/伪版本/本地 replace（安全包除外）；正式发布必须来自中央/私有仓库，版本号不可覆盖重发（保证发布幂等）。
3. 【强制】新增或升级依赖：非必要不改既有依赖版本；确需升级必须评估并对比升级前后依赖树（Maven `dependency:tree`、`pipdeptree`、`npm ls`、`go mod graph`），冲突用 exclude/override 显式仲裁。
4. 【强制】同一 group 的成套依赖用统一版本变量（`${spring.version}` / monorepo 统一版本）；同一依赖在各子模块版本必须一致。
5. 【强制】库的接口出入参禁止使用枚举类型（Java 序列化兼容性）；参数可以用。
6. 【推荐】声明放 dependencies、版本收敛到 dependencyManagement（Maven）；对应：npm 用 lockfile + resolutions，Python 用约束文件，Go 用 go.mod 最小版本选择。
7. 【参考】发布库的两原则：精简可控（剥离一切非必要 API 与依赖，只依赖日志门面不绑实现，必须携带的依赖 scope 设 provided）；稳定可追溯（每版记 changelog，可查 owner 与源码）。

## 服务器（参考项）

1. 【推荐】高并发服务的内核调优要分清参数职责：`net.ipv4.tcp_fin_timeout` 控制的是**孤儿连接停留在 FIN_WAIT_2 的时长**，不是 TIME_WAIT；TIME_WAIT 停留时长在内核中是编译期常量（约 60s），缓解 TIME_WAIT 堆积用 `net.ipv4.tcp_tw_reuse = 1`（仅出站连接生效）、开启连接复用（HTTP keep-alive / 连接池）从源头减少短连接。另调大文件描述符上限（默认 1024 远不够，一连接一 fd）。
2. 【推荐】JVM 加 `-XX:+HeapDumpOnOutOfMemoryError`；Go 服务开 pprof 端点（内网）；Python 服务配好 faulthandler——OOM/崩溃现场信息价值极高。
3. 【参考】站内跳转用 forward，站外重定向经 URL 拼装工具（防不一致与安全风险）。
