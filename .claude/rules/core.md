# 通用编程规约（跨语言常驻）

Java 特有部分见 backend/java.md。

## 命名

1. 【强制】类/类型名 UpperCamelCase；方法、变量、参数 lowerCamelCase（Python/Go 遵循各自语言规范，见对应 rules）；常量全大写下划线分隔，语义完整（`MAX_STOCK_COUNT`，而非 `MAX_COUNT`）。
2. 【强制】禁止不规范缩写（`condi`、`AbsClass`）；布尔命名按语言各自惯例，不做跨语言统一：Java POJO 成员禁 `is` 前缀（序列化陷阱，见 java.md 命名#5）；Python 用 `is_xxx`/`has_xxx`；TS/JS 用 `isXxx`/`hasXxx`；Go 惯用 `enabled`/`ready`/`hasAccess`，不强制前缀；DB 列名一律 `is_xxx`。
3. 【强制】抽象类 `Abstract`/`Base` 开头；异常类 `Exception`/`Error` 结尾；测试类 `被测类名 + Test`。
4. 【推荐】使用设计模式时体现在类名中：`OrderFactory` / `LoginProxy` / `ResourceObserver`。
5. 【参考】数据访问层方法前缀约定：查单个 `get`、查列表 `list`、统计 `count`、插入 `insert/save`、删除 `delete/remove`、更新 `update`。

## 常量

1. 【强制】禁止魔法值；`long` 字面量用大写 `L` 后缀。
2. 【推荐】常量按功能归类到不同常量模块（如 `CacheConsts` / `ConfigConsts`），不做一个巨型常量类。
3. 【推荐】取值固定范围或带属性的值，用枚举而非零散常量。

## 格式

> 前提：项目配置了官方/统一 formatter（gofmt、ruff format、Prettier、IDE 格式化方案）时，**以 formatter 输出为准**，本节仅在无 formatter 约束的场景下适用——规约不与格式化器互相改写。

1. 【强制】K&R 大括号风格：左括号不换行、其后换行；`else` 紧跟 `}`。运算符两侧、关键字与括号之间各留一个空格；逗号后留空格。
2. 【强制】单行不超过 120 字符（Python 项目若 formatter 配置为 88 则从其配置）；换行时第二行缩进 4 空格，运算符/`.` 随后文一起换行，多参数在逗号后换行。
3. 【推荐】不做多变量空格对齐；相同语义的代码段之间用单个空行分隔即可。

## 流程控制

1. 【强制】`switch` 每个 case 以 break/return 结束或注明贯穿意图，必须有 default（哪怕为空）；Python `match` 同理需兜底。
2. 【强制】if/for/while 即使单行也必须带大括号（Python 除外）。
3. 【推荐】少用 else，多用卫语句提前 return；嵌套不超过 3 层。
4. 【推荐】复杂条件表达式先赋给语义化布尔变量，再进 if。
5. 【推荐】循环体内不做：对象/变量声明、建连接、try-catch——移到循环外。
6. 【参考】参数校验的取舍：对外接口（RPC/HTTP/OpenAPI）、权限相关、低频方法必须校验；高频底层方法、私有可控方法可豁免但要注明由调用方保证。

## 注释

1. 【强制】公开类型与方法写文档注释（Javadoc / docstring / godoc / JSDoc），说明功能、参数、返回值、可能的异常。
2. 【强制】代码逻辑变更时同步更新注释，尤其是参数、返回值、异常与核心逻辑。
3. 【强制】TODO/FIXME 必须带作者与日期，定期清扫。
4. 【推荐】注释解释 why 而非 what；命名与结构自解释的代码不加废注释（`// put elephant into fridge`）。专有名词与关键字保留英文，其余可用中文。

## 其他

1. 【强制】正则须预编译为常量，不在方法体/热路径内反复编译。
2. 【强制】取当前时间戳用系统毫秒接口（Java `System.currentTimeMillis()`、Python `time.time()`、Go `time.Now()`、JS `Date.now()`），不 new 完整对象再取。
3. 【推荐】数据结构初始化尽量指定容量，避免无界增长。
