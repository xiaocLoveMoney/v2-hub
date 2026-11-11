---
paths:
  - "**/*.ts"
  - "**/*.tsx"
  - "**/*.js"
  - "**/*.jsx"
  - "**/*.vue"
---

# 前端规约（TS / JS / Vue / React）

通用条目见 core.md，安全见 security.md（XSS/CSRF 条目对前端强约束）。

## 命名与常量

1. 【强制】变量/函数 camelCase，类/组件/类型 PascalCase，常量 UPPER_SNAKE；文件名：组件 PascalCase 或 kebab-case（项目内统一），其余 kebab-case。
2. 【强制】禁止魔法值：接口路径、状态码、事件名、storage key 全部提取常量/枚举模块（对应 CacheConsts 分类思路）。
3. 【强制】布尔命名 `isXxx`/`hasXxx`/`canXxx`；事件处理 `handleXxx`/`onXxx`。

## 语言基础（对应 OOP/集合规约）

1. 【强制】新代码一律 TypeScript；禁止 `var`，默认 `const`，需重赋值才 `let`（对应 final 优先）。
2. 【强制】判等一律 `===`/`!==`；浮点判等用误差范围，金额用整数分单位或 decimal 库运算，禁止 `0.1 + 0.2` 式直接比较。
3. 【强制】公开函数/组件 props 必须有类型定义；`strict: true`；禁止裸 `any`（确需时 `unknown` + 收窄，或注释原因）。
4. 【强制】可空处理显式化：`x?: T`、`T | null` 声明清楚，访问链用 `?.` + 兜底，禁止不判空的 `a.b.c.d`（对应 NPE 高危链式调用条）。
5. 【强制】不修改已导出函数签名；废弃 API 用 `@deprecated` JSDoc 注明替代方案。
6. 【强制】禁止在遍历中增删原数组；用 `filter`/`map` 产生新数组（不可变优先）。
7. 【推荐】自定义对象判重不能依赖引用相等——Map/Set 存对象时明确 key 策略（序列化 key 或用 id）。
8. 【推荐】大量字符串拼接用数组 join 或模板字符串一次成型。

## 异步（对应并发规约）

1. 【强制】统一 async/await；每条 await 链必须有错误处理（try-catch 或上层统一边界），禁止裸浮空 Promise（no-floating-promises）。
2. 【强制】并发请求控制数量（`Promise.all` 分批 / p-limit），禁止循环内无界并发发请求（对应线程池限流思想）。
3. 【强制】组件卸载时清理副作用：定时器、事件监听、AbortController 取消未完成请求（对应 ThreadLocal remove 的清理精神）。
4. 【推荐】接口层封装统一的重试/超时/取消；竞态用 AbortController 或请求序号丢弃过期响应。

## 异常与日志

1. 【强制】不吞错误：catch 里禁止空块；要么处理（用户提示/降级），要么带上下文上抛/上报。
2. 【强制】全局错误边界：window.onerror/unhandledrejection + 框架级 ErrorBoundary（React）/errorHandler（Vue），对应"顶层必须处置并转用户可懂信息"。
3. 【强制】提交代码不留 `console.log` 调试语句；日志/上报统一走封装（分级、采样、脱敏）。
4. 【推荐】对外错误展示友好文案 + 错误码，不透出堆栈（对应 Web 层不抛异常、跳转友好错误页）。

## Vue / React

1. 【强制】props 单向数据流，禁止子组件直接改 props；跨层状态经 store（Pinia/Redux/Zustand），禁止事件总线满天飞。
2. 【强制】列表渲染 key 用稳定唯一 id，禁止用 index 作 key（有增删/排序时）。
3. 【强制】禁止未消毒内容进 `v-html`/`dangerouslySetInnerHTML`（security.md 第 5 条）。
4. 【推荐】组件单一职责，超过 ~300 行或多于一个关注点即拆分；逻辑复用用 composable/hook（对应 DRY 抽方法条）。
5. 【推荐】computed/衍生状态不写副作用（对应 getter 无业务逻辑条）。

## 工程

1. 【强制】依赖 lockfile 必须提交；升级依赖须评估，对比升级前后的依赖树。
2. 【强制】遵循项目 ESLint/Prettier 配置，不局部关规则不加注释说明。
3. 【推荐】请求/响应模型集中定义类型（对应 DO/DTO/VO 分层：API 响应类型 ≠ 视图模型，页面复杂时分开映射）。
