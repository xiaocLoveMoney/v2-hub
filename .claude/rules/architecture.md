# 应用架构规约（常驻）

> 原属 project.md。拆出原因：project.md 的 `paths` 只匹配构建文件（pom.xml、go.mod 等），
> 而这些是**编辑源码时**需要生效的架构规则——按路径挂在构建文件上根本加载不到。
> 本文件无 paths、常驻加载。后端通用，Java 语境命名，Python/Go 对应同构。

## 应用分层

依赖方向自上而下：

```
开放接口层(RPC/网关) → Web 层(控制器) → Service 层(业务逻辑)
→ Manager 层(通用能力/三方封装/缓存/多 DAO 组合) → DAO 层(数据访问) → DB/三方平台
```

1. 【推荐】各层职责：Web 层做转发、参数基础校验、不可复用的简单业务；Service 承载具体业务；Manager 沉淀通用处理（三方服务封装并预处理返回值与异常、缓存方案、中间件通用处理、多 DAO 组合复用）；DAO 只做数据交互。小型项目可合并 Service 与 Manager，但依赖方向不得反转。
2. 【参考】异常分层处置：DAO 层 `catch (Exception e) { throw new DAOException(e); }` 不打日志；Service/Manager 必须打日志且带尽量全的参数上下文；Web 层不得向上抛异常，渲染失败跳转友好错误页；开放接口层转错误码 + 错误信息。
3. 【参考】领域模型：DO（对表）、DTO（Service/Manager 向上传输）、BO（业务对象）、Query（查询对象，>2 个条件禁用 Map 传参）、VO（展示层）。FastAPI 对应 = ORM Model / pydantic Schema 分层；Go 对应 = entity / dto / vo 包。
