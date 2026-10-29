# Go 代码审查清单

> 本文件是详细审查清单，按主题组织。审查时根据代码涉及的主题按需查阅对应章节，不必每次遍历全部。

> **审查职责边界**：标记为 `🔧` 的检查项由确定性工具负责（gofumpt/golangci-lint），
> AI 审查时跳过这些项。完整工具覆盖范围见 review SKILL Step 3a。

## 目录

- [代码风格与命名](#代码风格与命名)
- [文档与注释](#文档与注释)
- [Context 使用](#context-使用)
- [错误处理](#错误处理)
- [API 边界与参数防御](#api-边界与参数防御)
- [接口设计](#接口设计)
- [函数设计](#函数设计)
- [Receiver 选择](#receiver-选择)
- [变量设计](#变量设计)
- [返回值设计](#返回值设计)
- [字面量格式](#字面量格式)
- [指针使用](#指针使用)
- [空接口使用](#空接口使用)
- [并发安全](#并发安全)
- [Kafka Consumer 与状态管理](#kafka-consumer-与状态管理)
- [GORM 使用](#gorm-使用)
- [日志与监控](#日志与监控)
- [安全编码](#安全编码)
- [数据库规范](#数据库规范)
- [测试规范](#测试规范)
- [Import 规范](#import-规范)
- [包设计](#包设计)
- [泛型使用](#泛型使用)

---

## 代码风格与命名

- [ ] 🔧 gofumpt — 使用 gofumpt 格式化
- [ ] 命名使用 MixedCaps（非 snake_case）
- [ ] 包名简短、全小写、无下划线，避免 util/common/helper/model
- [ ] Receiver 名称为 1-2 字母缩写（一致性优先，禁止 this/self/me）
- [ ] 避免命名冗余：
  - 包名+符号名冗余 (`widget.NewWidget` → `widget.New`)
  - 方法名重复 receiver 类型 (`config.WriteConfigTo` → `config.WriteTo`)
  - 变量名+类型冗余 (`numUsers int` → `users int`)
  - 外部上下文+本地名称冗余
- [ ] 缩写词全大写或全小写（URL, ID, API, gRPC, iOS, DDoS）
- [ ] 变量名简短，作用域越小命名越短：
  - 小作用域 (1-7行): 单字母/单词 (`i`, `r`, `w`)
  - 中作用域 (8-15行): 短词 (`count`, `name`)
  - 大作用域 (15-25行): 多词 (`userCount`, `requestURL`)
- [ ] Getter 方法不加 Get 前缀 (`User()` 而非 `GetUser()`)；复杂操作可用 `Compute` 或 `Fetch`
- [ ] 常量使用 MixedCaps，禁止 `MAX_SIZE` 或 `kMaxSize`
- [ ] 常量名描述角色而非值 (`MaxPacketSize` 而非 `Twelve`)
- [ ] 返回值的函数用名词，执行操作的函数用动词

## 文档与注释

- [ ] 所有导出名称必须有 Doc Comment
- [ ] Doc Comment 以被描述对象名称开头，使用完整句子，句号结尾
- [ ] 每个包只有一个 Package Comment，紧接 package 声明
- [ ] 注释解释"为什么"而非"做了什么"
- [ ] 不要重述 Context 取消的显而易见行为；非标准行为才需文档
- [ ] 只读操作天然假定并发安全无需注释；非标准并发需文档说明
- [ ] 注释行长度建议 80 字符左右
- [ ] 参数文档：只文档化容易出错或非显而易见的参数，不要逐个枚举

## Context 使用

- [ ] `ctx context.Context` 作为函数第一个参数
- [ ] 不要将 Context 存储在结构体中（除非签名必须匹配接口）
- [ ] 不要传递 nil Context，使用 `context.TODO()` 或 `context.Background()`
- [ ] 不要创建自定义 Context 类型
- [ ] Context 取消行为如果非标准需要文档说明

## 错误处理

- [ ] 函数失败返回 error 作为最后一个返回值
- [ ] 错误字符串小写开头，无标点结尾（除非以导出名/专有名词开头）
- [ ] 使用 `errors.Is` / `errors.As` 匹配错误（禁止字符串匹配）
- [ ] 错误处理前置，正常流程不缩进到 else
- [ ] 业务逻辑禁止使用 panic（仅 init/must 函数或包内 recover 模式可用）
- [ ] 不要使用 `_` 忽略错误，除非明确不需要处理并加注释
- [ ] 避免带内错误（in-band errors），使用多返回值
- [ ] 导出函数返回 `error` 类型而非具体错误类型（避免 nil 指针问题）
- [ ] 错误包装使用 `%w` 置于末尾 (`[...]: %w`)
- [ ] 添加有用的上下文信息，避免重复底层错误已有信息
- [ ] 不要仅为标注失败而包装：`fmt.Errorf("failed: %v", err)` → 直接 `return err`
- [ ] **%v vs %w 决策**：
  - `%w` 用于调用方需要 `errors.Is`/`errors.As` 检查的场景
  - `%v` 用于系统边界（RPC/存储），隐藏内部实现细节
  - `%v` 用于创建全新的独立错误
- [ ] 返回错误时优先不同时记录日志（避免日志重复，让调用方决定）
- [ ] 程序初始化错误向上传播到 main，使用 `log.Exit` 而非 `log.Fatal`
- [ ] 定义错误变量 `var ErrXxx = errors.New()` 而非魔法字符串

### Panic 边界与 recover（增量补充）

- [ ] **panic 不可跨越包边界**：导出函数内部 panic 会让调用方无法预判；不可恢复的内部不变量违反限于未导出代码，`Must*` 命名要显式提示
- [ ] **`recover` 必须由 defer 直接调用**：`defer handler()`（handler 内部 recover）是无效的，必须是 `defer func(){ if r := recover(); r != nil {...} }()`
- [ ] **recover 后不要静默吞掉**：至少要打印栈（`debug.Stack()`）或转 error；静默 recover 让偶发故障无法定位
- [ ] **不要把 panic/recover 当业务错误用**：尤其是"用 panic 跳出深层循环再 recover"的控制流写法，性能差且难以维护
- [ ] **nil map 写入、nil 接口方法调用、已 close 的 channel 发送/重复 close** 三类必 panic；尤其注意初始化遗漏导致的 nil map
- [ ] **goroutine 内 panic 不被外层 recover 捕获**：`go func(){...}()` 必须在自己内部 recover
- [ ] **`http.Handler` 内的 panic 会断连**：中间件要 recover，否则单个请求拖垮处理器

## API 边界与参数防御

- [ ] **导出函数 nil 防御**：接收指针/接口/slice/map 参数的导出函数，nil 输入应返回 error 而非导致 panic
- [ ] **指针链式访问安全**：链式调用嵌套指针字段时（如手写结构体 `order.User.Address.City` 或 proto 直接字段访问 `req.GetUser().Address`），中间节点为 nil 会 panic。注意：proto 生成的 `GetXxx()` 方法本身是 nil-safe 的（返回零值），但直接访问字段或基于零值做业务判断仍可能出错
- [ ] **外部输入索引边界检查**：用于切片/map 操作的 index/offset 来自外部输入时，必须做边界校验
- [ ] **导出 vs 内部校验边界**：导出函数承担校验责任，内部 helper 可信任调用方——但信任边界需通过注释或函数命名明确
- [ ] **校验失败的报告方式**：导出函数校验失败返回 error（永远不 panic）；不可恢复的内部不变量违反可 panic（如 Must* 模式），但 panic 不可逃逸包边界

## 接口设计

- [ ] 接口定义在使用方包中，而非实现方
- [ ] 不要为了 mock 而预先定义接口 (YAGNI)
- [ ] 接口命名：单方法接口用 `-er` 后缀（Reader, Writer）
- [ ] 优先接受接口，返回具体类型
- [ ] 不要在使用前定义接口
- [ ] 不要导出用户不需要的接口
- [ ] 构造函数返回具体类型而非接口

## 函数设计

- [ ] **函数长度**：目标 5-15 行。超过 20 行时考虑拆分，但以下场景允许更长：
  - switch/case 分发逻辑（每个 case 简短独立）
  - 表驱动测试的 test case 定义
  - 线性流水线式处理（每步简单，无嵌套）
  - 判断标准是"读者能否一屏内理解整个函数"，而非机械计数行数
- [ ] **参数数量**：目标 2-3 个，超过时使用 Options struct。但以下场景可接受更多参数：
  - 构造函数接收必需的依赖注入参数
  - 标准库风格的函数签名（如 `io.Copy(dst, src)`）
  - 判断标准是"调用方是否容易混淆参数顺序或含义"
- [ ] 优先同步函数，让调用者决定并发
- [ ] Named Result Parameters 仅用于文档目的或 defer 中修改
- [ ] 避免 Naked Returns（无参数的 return），中大型函数必须显式
- [ ] 使用值传递而非指针（除非需要修改或大型结构体）
- [ ] 函数签名保持单行，避免缩进混淆
- [ ] Must 函数仅用于程序启动时或测试
- [ ] **避免缩进地狱**：`if` 嵌套建议不超过 2 层，提前返回错误。但以下场景可容忍更深嵌套：
  - 确实需要多条件组合判断且拆分反而降低可读性
  - 判断标准是"正常流程是否被错误处理挤到了右侧"
- [ ] **`value, err :=` 重复**：超过 1 次时考虑拆分为子函数
- [ ] 避免函数/方法命名重复包名或 receiver 类型名

## Receiver 选择

- [ ] 方法需要修改 receiver → 使用指针
- [ ] receiver 包含 sync.Mutex 等不可复制字段 → 使用指针
- [ ] receiver 是大型结构体或数组 → 使用指针
- [ ] receiver 是 map/func/channel → 不使用指针
- [ ] receiver 是小型值类型结构体 → 使用值
- [ ] 同一类型的方法 receiver 类型保持一致，不要混用

## 变量设计

- [ ] **作用域最小化**：变量声明尽量靠近使用位置
- [ ] **避免全局可变变量**：全局变量难以追踪状态
- [ ] **变量遮蔽**：避免在内层作用域重新声明同名变量
- [ ] **不要遮蔽标准包名**：大作用域中避免 `url := "..."` 遮蔽 `net/url`
- [ ] **命名长度与作用域成反比**：作用域越小，命名越短
- [ ] **返回新值而非修改**：优先返回新值，减少可变性
- [ ] **结构体零值安全**：如果零值会导致隐蔽 bug（如超时为 0 被解读为"无超时"），应使用构造函数强制初始化，或在方法中检测零值给出合理默认行为

## 返回值设计

- [ ] **定义错误变量**：使用 `var ErrXxx = errors.New()` 而非魔法字符串
- [ ] **NullItem 模式**：返回定义好的空值对象，而非 `Type{}`
- [ ] **返回具体类型**：让用户难以犯错，易于理解返回值
- [ ] **避免返回 nil 指针**：可能导致调用方 panic

## 字面量格式

- [ ] 外部包类型必须指定字段名
- [ ] 🔧 gofumpt — 闭合括号与开括号同级缩进
- [ ] 适当省略重复类型名（`[]*Type{{A: 1}}` 而非 `[]*Type{&Type{A: 1}}`）
- [ ] 零值字段可省略（增强可读性时，突出重要配置）

## 指针使用

- [ ] **避免扩展指针作用域**：传入指针后修改会影响原数据
- [ ] **存储副本而非外部指针**：`store.users[id] = &user` (副本) 而非 `store.users[id] = user` (外部指针)
- [ ] **返回副本而非内部指针**：防止外部修改内部状态
- [ ] **指针输入参数需谨慎**：相当于扩展变量作用域到未知范围
- [ ] **构造函数返回指针是安全的**：调用者控制作用域
- [ ] 不要传递指针仅为节省几个字节（小型不变结构体用值传递）

## 空接口使用

- [ ] **避免滥用 `interface{}`**：仅在必要时使用（如 JSON 解码）
- [ ] **封装 interface{} 操作**：对外提供类型安全的 wrapper
- [ ] **不要用 interface{} 实现泛型容器**：使用代码生成或泛型
- [ ] **类型断言集中处理**：不要让用户处理 interface{} 类型转换
- [ ] 类型断言使用双返回值形式 `v, ok := x.(T)`，单返回值形式 `x.(T)` 仅限已通过 type switch 或其他方式确认类型的场景

## 并发安全

- [ ] 禁止无限制开启 goroutine
- [ ] sync 类型通过指针传递（不可复制）
- [ ] WaitGroup.Add() 在 goroutine 启动前调用
- [ ] for range 循环变量正确处理（Go < 1.22）
- [ ] goroutine 生命周期必须明确可控，文档说明退出条件
- [ ] 不要复制包含指针方法的结构体
- [ ] 优先使用同步函数，异步在调用方控制
- [ ] 使用 context 控制 goroutine 取消
- [ ] 函数签名中使用 channel 方向标注：`func send(ch chan<- T)` / `func recv(ch <-chan T)` ——编译器阻止反向操作
- [ ] 长生命周期 goroutine 的 select 循环中禁止使用 `time.After`（每次迭代创建新 timer 不会被 GC 直到触发）——使用 `time.NewTimer` + `Reset()`
- [ ] 使用 `errgroup.Group` 管理需要错误传播和 context 取消的并发 goroutine 组
- [ ] "检查-然后-操作"序列（`if !exists { create }`）在并发场景下必须加锁保护或使用原子操作
- [ ] channel 只由发送方 close；接收方 close 或多个 goroutine close 同一 channel 会 panic
- [ ] 读写同一共享状态的所有位置必须走同一把锁/同一个同步原语（半同步等于没同步）
- [ ] 混合读写 map 必须有保护；`sync.Map` 仅适用于读多写少且 key 稳定的场景

### Goroutine 生命周期与泄漏（增量补充）

> 泄漏不会立刻报错，只会随时间耗内存/句柄，所以要逐个 goroutine 追问"它什么时候退出"。

- [ ] **每个 goroutine 都要能回答"谁负责关掉它"**：`go func(){...}()` 内部必须有 `<-ctx.Done()` / channel 接收 / 明确退出条件。只有 `for range ch` 的 goroutine 依赖发送方 close——若发送方没 close，它永久阻塞
- [ ] **取消信号必须串到底**：函数创建了子 context（`WithCancel/WithTimeout`），必须 `defer cancel()`，并且把 ctx 传给下游调用（DB、HTTP、工具），否则取消无法传播
- [ ] **从函数逃逸出去的 goroutine 不能捕获可能被取消的短生命周期 ctx**：用 `context.WithoutCancel` 派生（如"收尾写库"场景），避免收尾时 ctx 已失效导致写入必然失败
- [ ] **无人接收的发送会永久阻塞**：向无缓冲/已满 channel 发送且接收方已退出 = goroutine 泄漏。用 `select { case ch <- v: case <-ctx.Done(): }` 或带缓冲 + 丢弃策略
- [ ] **定时器资源**：`time.NewTicker` 必须 `defer ticker.Stop()`（放到 `select` 里 Timer 就废了）
- [ ] **goroutine 数量必须有上限**：循环内直接 `go f()` 等于无限并发，输入一大就 goroutine 爆炸；用 `errgroup.SetLimit` 或信号量约束
- [ ] **WaitGroup 计数不能变负，也不能在 Wait 返回后被复用**
- [ ] **后台 goroutine 内的 panic 会崩进程**：服务端/库代码里 `go func(){...}()` 必须就地 `defer recover()`，否则一个 nil 指针带走整个服务
- [ ] **测试里起的 goroutine 必须等到其退出**：否则 `-race` 与 goroutine 泄漏检查会误报/漏报

## 资源生命周期（增量补充）

- [ ] `resp.Body` 必须 `defer resp.Body.Close()`：即使不读 body 也要关，否则连接不复用、句柄耗尽
- [ ] 读外部响应要用 `io.LimitReader` 限制大小（防超大响应把进程打爆）
- [ ] `sql.Rows` / `os.File` / `net.Conn` / `Stmt` 使用后必须 Close（错误路径也要关，通常靠 `defer`）
- [ ] `context.WithCancel/WithTimeout` 返回的 cancel 必须被调用：否则父 ctx 及其 timer 泄漏
- [ ] 循环体内不要写 `defer`（攒到函数结束才释放，循环次数多就是泄漏）——移进 helper 函数或显式 Close
- [ ] `errgroup.WithContext` 派生的 ctx 在 Wait 之后已取消，不要再拿它做后续操作
- [ ] 文件/目录权限要显式写明：文件 0o600/0o644，目录 0o700/0o755（不依赖 umask 默认值）
- [ ] 临时文件要有 `defer os.Remove(tmp)` 兜底，避免异常路径残留
- [ ] 无上限的全局 map/slice 缓存等同于内存泄漏：必须有容量/TTL 约束

## Kafka Consumer 与状态管理

> 本章覆盖 Kafka Consumer 及类似消息消费架构中的高风险模式。当审查涉及 Consumer Group、Rebalance、Seq 跟踪、GapFill 等逻辑时查阅。

### Upsert 幂等性（🔴 高风险）

- [ ] **单调递增字段的 Upsert 必须用 GREATEST 保护**：`ON DUPLICATE KEY UPDATE seq = VALUES(seq)` 是无条件覆盖。当多个实例（或同一实例的不同 goroutine）并发写入同一行时，延迟到达的较小值会覆盖已写入的较大值，导致 DB 中的 seq 回退。正确写法：`seq = GREATEST(seq, VALUES(seq))`
- [ ] **唯一索引要覆盖所有分区维度**：如果 Upsert 的冲突键是 `(date, session_id)` 但读取时用 `WHERE partition_id = ?` 过滤，写入不更新 `partition_id` 会导致读取匹配不到数据返回零值
- [ ] **检查 Upsert 是否更新了所有需要的列**：`DoUpdates` / `SET` 子句遗漏字段（如 `partition_id`）在冲突时会保留旧值

### Consumer 与 Producer 的 Pipeline 状态断层（🔴 高风险）

- [ ] **识别 pipeline 两端的状态**：Consumer 侧的内存状态（如 expectSeq）和 Producer 侧的持久化状态（如 DB msg_seq）是否跟踪同一个进度？如果是，它们之间的 pipeline 延迟有多大？
- [ ] **Rebalance 后新实例从 DB 初始化**：如果 DB 值滞后于实际处理进度（因为定时刷新间隔、批写延迟等），新实例会从过旧的 seq 开始，触发不必要的大范围 GapFill
- [ ] **定时刷新间隔 vs Rebalance 频率**：如果 seq 刷新是 1s 定时器，而两次 Rebalance 间隔 < 1s，中间实例的处理进度对 DB 的贡献为零。Cleanup/Destroy 回调应主动触发最后一次 flush
- [ ] **跨实例并发写入竞态**：旧实例的 Producer pipeline drain 可能在新实例写入之后才到达 DB。如果 Upsert 无条件覆盖（缺少 GREATEST），旧实例会把 DB 中新实例写的较大值覆盖回较小值

### 错误路径的数据完整性（🔴 高风险）

- [ ] **批写失败后不能清空内存缓存**：如果 "推送下游 → 批写 DB → 标记已处理" 是一个序列，批写 DB 失败时必须保留内存缓存中的数据以便重试。如果失败后仍清空缓存并标记已处理（推进 seq），数据会不可恢复地丢失——下游已收到消息但 DB 没有记录，后续重推历史时会缺失
- [ ] **markOutSessionSeq / 推进 seq 必须在落库成功之后**：错误分支中不应推进任何表示"已完成"的标记

### GapFill 与补偿机制

- [ ] **GapFill 必须可中断**：如果 GapFill 通过无缓冲 channel 逐条推送报告，长时间 GapFill 会阻塞 Destroy()。Kafka session.timeout.ms 默认 10s，超时后 Broker 认为 Consumer 挂了会触发级联 Rebalance。GapFill 循环中应检查 context 取消或退出信号
- [ ] **GapFill 期间丢弃消息的级联效应**：如果 GapFill 期间正常消息被丢弃（channel 满），丢弃的消息又造成新 gap，可能形成 "GapFill → 丢消息 → 新 Gap → GapFill" 循环
- [ ] **GapFill 不完整时不应直接跳过中间 seq**：如果 GapFill 服务返回数据不完整（部分消息延迟），直接将 expectSeq 跳到最新 seq 会导致中间的 seq 永久丢失。应记录 pending 范围由定时补偿机制后续处理
- [ ] **本地缓存是否跨实例共享**：如果 refillCache / dedup 缓存是本地内存，Rebalance 后新实例缓存为空，可能触发大量无效的历史数据重查和重推

### Kafka Rebalance 生命周期

- [ ] **Setup/Cleanup 回调中是否有必要的初始化和清理**：仅打 log 不够。Cleanup 应触发 seq flush、取消正在进行的 GapFill
- [ ] **Destroy 等待不应无超时**：`close(ch) + <-doneCh` 模式在 doneCh 信号源被阻塞时会永久等待。应加 context 超时或 select 超时
- [ ] **快速连续 Rebalance 的极端场景**：两次 Rebalance 间隔 < 1s 时，中间实例几乎不会向 DB 写入任何进度。第三个实例读到的 DB 值可能是第一个实例的旧值，完全不知道中间实例的存在

## GORM 使用

- [ ] 禁止缓存 DB 指针
- [ ] update 明确指定字段（禁止模型默认更新）
- [ ] insert 每个字段显式赋值（注意零值与 default 不一致）
- [ ] 禁止使用 `Save()` 方法
- [ ] 使用 `errors.Is(err, gorm.ErrRecordNotFound)` 处理未找到
- [ ] 事务使用 `defer tx.Rollback()` + 成功路径 `tx.Commit()`（Commit 后 Rollback 是 no-op）
- [ ] 不要在 defer 中无条件 Commit——错误路径下会提交不完整数据

## 日志与监控

- [ ] 使用结构化日志 API（`log.Error` 而非 `log.Errorf`）
- [ ] Error 级别日志伴随监控上报
- [ ] 命名规则：`{模块}_{操作}_{结果}`（如 `QueryOrder_GetDB_Err`）
- [ ] 谨慎使用 log.Error（会触发 flush，影响性能；应可操作而非仅"严重"）
- [ ] 避免日志重复（返回错误让调用方处理，不要同时 log 和 return err）
- [ ] 注意 PII（个人身份信息）不要记录到日志
- [ ] 日志 message 必须是静态字面量，变量数据通过结构化字段传递

## 安全编码

- [ ] 禁止硬编码密码/密钥
- [ ] 使用 `crypto/rand` 生成密钥（禁止 `math/rand`）
- [ ] 文件操作检查路径穿越
- [ ] 外部输入拼接 HTML 需编码

### 注入与外部输入（增量补充）

- [ ] **命令注入**：禁止把外部输入拼进 `sh -c` / `Command("bash", "-c", ...)`。必须参数化：`exec.Command(name, arg1, arg2...)`，参数逐个传入
- [ ] **命令执行必须有白名单或高危规则拦截**，`exec.Command` 出现在业务代码里就该追问：不可信输入能否到达参数？
- [ ] **路径穿越**：用户输入 + `filepath.Join(base, user)` 仍可能被 `../` 逃出（尤其是 Join 后的 Clean 与符号链接）；应对解析后的绝对路径做 `strings.HasPrefix(filepath.Clean(abs), base+string(os.PathSeparator))` 校验
- [ ] **符号链接逃逸**：检查 base 到目标之间是否 symlink 指向外部（给敏感目录做 EvalSymlinks 再比对）
- [ ] **SSRF**：请求用户提供的 URL 前必须做 DNS 解析 + 内网/回环/元数据地址（169.254.169.254、127.0.0.0/8、10/8、172.16/12、192.168/16）拦截，且禁止跟随到内网的 redirect
- [ ] **禁止关闭 TLS 校验**：`InsecureSkipVerify: true` 只能出现在明确的临时测试场景并附注释
- [ ] **超长/超大输入**：外部响应、上传文件、工具输出统一做 size limit（`io.LimitReader`），并校验 Content-Length
- [ ] **日志/错误信息不得泄漏敏感值**：token、密码、私钥、完整内部错误堆栈不要打进 log
- [ ] **外部可控数据进 SQL**：必须参数化查询或预编译，禁止 `fmt.Sprintf` 拼 SQL
- [ ] **Zip Slip / 解压炸弹**：解压外部压缩包时校验条目路径是否在目标目录内，并限制条目数量与总解压字节数

## 数据库规范

- [ ] 禁止使用外键、触发器、存储过程
- [ ] 建表 NOT NULL 且设置默认值
- [ ] 查询按需指定字段（禁止 SELECT *）
- [ ] 新查询通过 EXPLAIN 评估

## 测试规范

- [ ] 测试失败信息格式：`YourFunc(%v) = %v, want %v`（顺序：actual != expected）
- [ ] 包含有用的上下文：输入、实际输出、期望输出
- [ ] 使用 Table-Driven Tests 覆盖多种情况
- [ ] 不使用断言库，让测试函数控制流程
- [ ] 使用 cmp.Equal/cmp.Diff 比较复杂结构
- [ ] 测试语义而非错误字符串
- [ ] 优先 t.Error 继续执行，t.Fatal 用于后续无意义时
- [ ] 子测试名称简短、可过滤、无斜杠
- [ ] **并发相关的改动必须有 `-race` 用例**：`go test -race ./pkg/...`，构造并发调用或 goroutine 交叠，让修复可被复现且防止回归
- [ ] **泄漏类缺陷要用 goroutine 数或资源计数断言**：修复泄漏前先有失败的用例
- [ ] **错误路径同样要有用例**：出 bug 的往往是 return 分支，而不是正常路径
- [ ] **测试之间互相隔离（hermetic）**：不要共用同一个临时目录/DB 文件/session ID，否则彼此污染形成偶发失败
- [ ] **测试不能污染工作区**：需要落盘时用 `t.TempDir()` 注入的路径，禁止写进 CWD
- [ ] diff 使用 `(-want +got)` 格式并说明方向
- [ ] 测试辅助包命名：原包名 + "test" 后缀（如 `creditcardtest`）

## Import 规范

- [ ] 按组排序：标准库、外部包、proto（pb 后缀）、side-effect
- [ ] 避免 import renaming，除非名称冲突
- [ ] proto 包重命名加 `pb` 后缀
- [ ] 禁止 import `.`（使代码难以理解来源）
- [ ] blank import 仅在 main 包或必要测试中
- [ ] 冲突时优先重命名最本地/项目特定的包

## 包设计

- [ ] 包大小适中，概念内聚（不要所有代码放一个包，也不要每个类型一个包）
- [ ] 避免在大作用域中用变量遮蔽标准包名
- [ ] 文件组织：相关代码放同一文件，避免单文件过大
- [ ] 构造函数返回具体类型，不返回接口（让消费方定义接口）
- [ ] 避免使用 `init()` 函数——`init()` 在 import 时执行、不能返回错误、使测试困难、创建隐式依赖。优先使用显式的构造函数或 `sync.Once`

## 泛型使用

- [ ] 仅在满足实际业务需求时使用
- [ ] 不要用泛型发明 DSL 或错误处理框架
- [ ] 如果只有一种类型实例化，先不用泛型
- [ ] 优先使用接口而非泛型（如果合适）
