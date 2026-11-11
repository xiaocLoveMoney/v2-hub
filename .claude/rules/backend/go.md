---
paths:
  - "**/*.go"
---

# Go 规约（Effective Go / Google Style）

命名与格式以 gofmt 与 Go 官方规范为准（Tab 缩进例外于 core.md）；通用条目见 core.md。

## 命名

1. 【强制】包名全小写单个单词、不含下划线、单数；导出标识符 PascalCase，未导出 camelCase；常量同普通命名，不用 UPPER_SNAKE（Go 惯例）。
2. 【强制】接口按能力命名，单方法接口用 `-er` 后缀（`Reader`/`Translator`）；禁止 `IUser` 式匈牙利前缀。
3. 【强制】错误变量 `ErrXxx`、错误类型 `XxxError`（对应异常类命名规约）；测试函数 `TestXxx` 与被测函数对应。
4. 【强制】禁止拼音、生僻缩写；缩略词保持一致大小写（`userID`/`ServeHTTP`，非 `userId`）。

## 类型与结构（对应 OOP 规约）

1. 【强制】浮点判等禁 `==`，用误差范围；金额用整数最小单位（分）或 decimal 库，禁 float64 存钱。
2. 【强制】"POJO 用包装类表达可空"的对应做法：需要区分零值与未赋值时用指针字段 `*int64` / `sql.NullXxx`，并在文档注明 nil 语义；否则用零值。
3. 【强制】构造遵循 Go 惯例：`func NewXxx(cfg Config) (*Xxx, error)`——创建成功即有效、失败直接返回 error；**不做** `NewXxx()` + `Init()` 两阶段公开初始化（制造"已创建未就绪"中间态，也不符合标准库风格）。
4. 【强制】不修改已发布函数签名；废弃 API 用 `// Deprecated: 用 Xxx 替代` 注释。
5. 【推荐】接收者类型统一（同一类型的方法不混用值/指针接收者）；小接口优于大接口，在使用方定义接口（隐式实现）。
6. 【推荐】循环内字符串拼接用 `strings.Builder`；slice/map make 时给容量。

## 集合

1. 【强制】map 并发读写必须加锁（`sync.RWMutex`）或用 `sync.Map`——并发写裸 map 直接 panic（对应 HashMap 并发风险条）。
2. 【强制】迭代中删 map 键是允许的，但迭代中对 slice 增删须操作副本或倒序索引。
3. 【推荐】slice 截取共享底层数组，向子 slice append 可能污染原数组——需要隔离时显式 copy（对应 subList 视图陷阱）。

## 并发

1. 【强制】goroutine 不能无界创建：用 worker pool / `semaphore` / `errgroup.SetLimit` 控制并发数（对应"线程必须池化、禁 Executors 无界"）。
2. 【强制】每个 goroutine 必须有明确退出路径，禁止泄漏：用 `context` 传取消，阻塞 channel 配 `select + ctx.Done()`。
3. 【强制】谁启动谁负责：goroutine 内必须 `recover` 兜底或保证不 panic——一个未捕获 panic 杀死整个进程（比 Timer 杀线程更严重）。
4. 【强制】锁的获取与释放紧邻：`mu.Lock(); defer mu.Unlock()`；多把锁全局固定顺序防死锁。
5. 【强制】计数器用 `atomic` 包，禁止裸 `count++` 跨 goroutine（对应 volatile/AtomicInteger 条）。
6. 【强制】共享记录并发更新：DB 层乐观锁（version 列，冲突 <20%，重试 ≥3）或悲观锁。
7. 【推荐】定时任务用 `time.Ticker` + 显式 Stop 或 cron 库，任务体自行 recover（对应 ScheduledExecutorService 条）；select 循环中的 `time.After`：Go 1.23 前 timer 到期才释放（长超时高频循环下堆积），1.23 起未引用的 timer 可被 GC、但每轮仍新建 timer 有分配开销——热路径统一用 Ticker 或 Timer.Reset。

## 错误处理（对应异常规约）

1. 【强制】error 必须处理，禁止 `_ = err` 丢弃（确需忽略要注释原因）；不吞错误：要么处理，要么 `fmt.Errorf("上下文: %w", err)` 包装上抛。
2. 【强制】panic 仅用于程序性错误（初始化失败、不可能分支），业务错误一律 error；库代码禁止向调用方 panic。
3. 【强制】判定错误用 `errors.Is`/`errors.As`，禁止字符串匹配错误信息。
4. 【强制】资源关闭用 `defer`，并检查 Close 的 error（至少写文件场景）；defer 中禁止再 panic。
5. 【推荐】对外 HTTP/RPC 统一错误码结构；应用内传 error 链，顶层统一转用户可读信息。

## 日志

1. 【强制】统一结构化日志门面（`log/slog` 或 zap），业务代码禁 `fmt.Println`/`log.Print` 散打。
2. 【强制】记录错误带上下文字段与错误链：`slog.Error("query user", "uid", uid, "err", err)`；不处理就上抛，不重复打印。
3. 【推荐】级别约定同 Java 条；生产禁 Debug 常开。

## 数据库与工程

1. 【强制】SQL 参数化 `db.Query("... WHERE id=?", id)`，禁 fmt.Sprintf 拼 SQL；遵循 mysql.md 全部规则。
2. 【强制】依赖用 go.mod 锁定；**发布/生产依赖**优先打了 tag 的语义化版本（首版 v1.0.0，破坏性升 major），不长期停留在指向未打标提交的 pseudo-version 上（pseudo-version 是 Go Modules 的合法机制，临时引用可以，进入线上依赖清单前应推动上游打 tag 或 fork 打 tag）；禁止 replace 指向本地路径进入发布分支。
3. 【推荐】提交前 `gofmt`、`go vet`、`golangci-lint` 过一遍（lint.sh 会自动跑已安装的）。
