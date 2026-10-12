# Go Code Review - 代码示例参考

本文件包含常见 Go 代码审查问题的正反示例，供审查时快速参考。

## 目录

- [错误处理](#错误处理) — 错误匹配、字符串格式、%v vs %w、包装、日志重复、错误变量、返回类型、流程缩进
- [GORM 使用](#gorm-使用) — 更新字段
- [日志与监控](#日志与监控) — 结构化日志
- [命名规范](#命名规范) — 基本命名、缩写词、函数名重复、命名惯例
- [Context 使用](#context-使用) — 结构体存储、参数位置
- [接口设计](#接口设计) — 使用方定义接口
- [Receiver 类型选择](#receiver-类型选择) — Mutex 相关
- [并发](#并发) — for range 循环、goroutine 生命周期
- [字面量与声明](#字面量与声明) — 空切片、结构体字面量、零值、类型省略
- [函数设计](#函数设计) — 缩进地狱、参数过多
- [指针使用](#指针使用) — 指针作用域扩展
- [变量遮蔽](#变量遮蔽) — 变量遮蔽、标准包名、Context 遮蔽
- [空接口封装](#空接口封装) — 类型安全 wrapper
- [nil 值保护](#nil-值保护) — getter 保护
- [文档与注释](#文档与注释) — Doc Comment、Context 文档、并发安全文档
- [测试](#测试) — 失败信息、辅助包命名
- [Panic 与 Recovery](#panic-与-recovery) — 内部 panic+recover、不可达代码
- [switch 与 break](#switch-与-break)
- [crypto/rand 使用](#cryptorand-使用)
- [闭包灵活性](#闭包灵活性)
- [带内错误](#带内错误in-band-errors)
- [参数防御](#参数防御)
- [并发（扩展）](#并发扩展)

---

## 错误处理

### 错误匹配
```go
// ❌ 错误：字符串匹配
if strings.Contains(err.Error(), "not found") { ... }

// ✅ 正确：使用 errors.Is
if errors.Is(err, gorm.ErrRecordNotFound) { ... }
```

### 错误字符串格式
```go
// ❌ 错误：大写开头、标点结尾
err := fmt.Errorf("Something bad happened.")

// ✅ 正确：小写开头、无标点
err := fmt.Errorf("something bad happened")
```

### 错误包装位置
```go
// ❌ 错误：%w 在中间
err := fmt.Errorf("%w: additional context", originalErr)

// ✅ 正确：%w 在末尾
err := fmt.Errorf("additional context: %w", originalErr)
```

### %v vs %w 选择
```go
// ✅ 系统边界（RPC/存储）用 %v 隐藏内部错误
func (s *Server) GetUser(ctx context.Context, req *pb.Request) (*pb.Response, error) {
    user, err := s.db.FindUser(ctx, req.GetId())
    if err != nil {
        return nil, fmt.Errorf("couldn't find user: %v", err) // %v: 不暴露内部错误
    }
    return toProto(user), nil
}

// ✅ 内部函数间用 %w 保留错误链
func (s *service) findUser(ctx context.Context, id string) (*User, error) {
    user, err := s.repo.Get(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("find user %s: %w", id, err) // %w: 调用方可 errors.Is
    }
    return user, nil
}
```

### 无用的错误包装
```go
// ❌ 错误：仅标注"failed"，无附加信息
return fmt.Errorf("failed: %v", err)

// ✅ 正确：直接返回
return err

// ✅ 正确：如果要包装，添加有用上下文
return fmt.Errorf("query user %s: %w", userID, err)
```

### 避免错误信息重复
```go
// ❌ 错误：重复底层已有的路径信息
if err := os.Open("settings.txt"); err != nil {
    return fmt.Errorf("could not open settings.txt: %v", err)
}
// Output: could not open settings.txt: open settings.txt: no such file or directory

// ✅ 正确：添加上层有用的上下文
if err := os.Open("settings.txt"); err != nil {
    return fmt.Errorf("launch codes unavailable: %v", err)
}
// Output: launch codes unavailable: open settings.txt: no such file or directory
```

### 日志重复
```go
// ❌ 错误：返回错误又记录日志（调用方也会记录）
func getUser(ctx context.Context, id string) (*User, error) {
    user, err := db.Query(ctx, id)
    if err != nil {
        log.Error(ctx, "getUser failed", log.ErrorField(err)) // 重复！
        return nil, err
    }
    return user, nil
}

// ✅ 正确：只返回错误，让调用方决定如何处理
func getUser(ctx context.Context, id string) (*User, error) {
    user, err := db.Query(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("get user %s: %w", id, err)
    }
    return user, nil
}
```

### 定义错误变量
```go
// ❌ 错误：魔法字符串，调用方无法可靠匹配
func GetItem(id string) (Item, error) {
    if !ok {
        return Item{}, errors.New("item could not be found")
    }
    return item, nil
}

// ✅ 正确：定义错误变量
var (
    NullItem        = Item{}
    ErrItemNotFound = errors.New("item could not be found")
)

func GetItem(id string) (Item, error) {
    if !ok {
        return NullItem, ErrItemNotFound
    }
    return item, nil
}

// 调用方使用 errors.Is 匹配
if errors.Is(err, store.ErrItemNotFound) {
    // 处理未找到
}
```

### 函数返回错误类型
```go
// ❌ 错误：返回具体错误类型（nil 指针问题）
func Bad() *os.PathError { return nil }

// ✅ 正确：返回 error 接口
func Good() error { return nil }
```

### 错误流程缩进
```go
// ❌ 错误：正常流程在 else 中
if err != nil {
    // 错误处理
} else {
    // 正常流程（缩进过深）
}

// ✅ 正确：错误处理后提前返回
if err != nil {
    return err
}
// 正常流程（无缩进）
```

## GORM 使用

### GORM 更新
```go
// ❌ 错误：模型默认更新（零值无法更新）
db.Model(&user).Updates(user)

// ✅ 正确：明确指定字段
db.Model(&user).Updates(map[string]interface{}{
    "name": "new_name",
    "active": false,
})
```

### GORM 事务
```go
// ❌ 错误：错误路径未 Rollback
func CreateOrderWithItems(db *gorm.DB, order *Order, items []Item) error {
    tx := db.Begin()
    if err := tx.Create(order).Error; err != nil {
        return err // BUG: tx 未 Rollback，连接可能泄漏
    }
    for _, item := range items {
        if err := tx.Create(&item).Error; err != nil {
            return err // BUG: order 已写入但 items 不完整，且 tx 未 Rollback
        }
    }
    return tx.Commit().Error
}

// ✅ 正确：defer Rollback 保证清理
func CreateOrderWithItems(db *gorm.DB, order *Order, items []Item) error {
    tx := db.Begin()
    defer tx.Rollback() // Commit 后 Rollback 是 no-op

    if err := tx.Create(order).Error; err != nil {
        return fmt.Errorf("create order: %w", err)
    }
    for _, item := range items {
        if err := tx.Create(&item).Error; err != nil {
            return fmt.Errorf("create item: %w", err)
        }
    }
    return tx.Commit().Error
}
```

## 日志与监控

### 日志记录
```go
// ❌ 错误：格式化日志
log.Errorf(ctx, "处理失败, err:%v", err)

// ✅ 正确：结构化日志 + 监控上报
log.Error(ctx, "QueryOrder_GetDB_Err", log.ErrorField(err))
metric.EventsTotal.WithLabels(
    label.EventName, "QueryOrder_GetDB_Err",
    label.EventLevel, "alert",
).Inc()
```

## 命名规范

### 基本命名
```go
// ❌ 错误
const MAX_SIZE = 100        // 非驼峰
type DBConnection struct{}  // 与包名冗余
var numUsers int            // 类型冗余
func (s *Server) GetUser()  // 不需要 Get 前缀

// ✅ 正确
const MaxSize = 100
type Connection struct{}
var users int
func (s *Server) User()
```

### 缩写词
```go
// ❌ 错误
xmlApi, Grpc, appId, Url

// ✅ 正确 (全大写或全小写)
xmlAPI, gRPC, appID, URL (导出) / url (未导出)
```

### 避免函数名重复
```go
// ❌ 错误：函数名重复包名
package yamlconfig
func ParseYAMLConfig(input string) (*Config, error)

// ✅ 正确
package yamlconfig
func Parse(input string) (*Config, error)

// ❌ 错误：方法名重复 receiver 类型
func (c *Config) WriteConfigTo(w io.Writer) (int64, error)

// ✅ 正确
func (c *Config) WriteTo(w io.Writer) (int64, error)
```

### 函数命名惯例
```go
// 返回值的函数用名词
func (c *Config) JobName(key string) (value string, ok bool)

// 执行操作的函数用动词
func (c *Config) WriteDetail(w io.Writer) (int64, error)
```

## Context 使用

```go
// ❌ 错误：Context 存储在结构体中
type Server struct {
    ctx context.Context // 不要这样做
}

// ❌ 错误：Context 不是第一个参数
func DoSomething(data string, ctx context.Context) error { ... }

// ✅ 正确：Context 作为第一个参数
func DoSomething(ctx context.Context, data string) error { ... }
```

## 接口设计

```go
// ❌ 错误：在实现方预先定义接口
// package producer
type DataStore interface { // 不要预先定义
    Get(id string) (Data, error)
    Put(data Data) error
}
type dataStore struct { ... }

// ✅ 正确：在使用方定义接口，返回具体类型
// package producer
type DataStore struct { ... }
func NewDataStore() *DataStore { ... } // 返回具体类型，不是接口

// package consumer
type DataGetter interface { // 只定义需要的方法
    Get(id string) (Data, error)
}
func ProcessData(store DataGetter) { ... }
```

## Receiver 类型选择

```go
// ❌ 错误：包含 Mutex 但使用值 receiver
type SafeCounter struct {
    mu    sync.Mutex
    count int
}
func (c SafeCounter) Inc() { // 值 receiver 会复制 Mutex
    c.mu.Lock()
    c.count++
    c.mu.Unlock()
}

// ✅ 正确：使用指针 receiver
func (c *SafeCounter) Inc() {
    c.mu.Lock()
    c.count++
    c.mu.Unlock()
}
```

## 并发

### for range 循环
```go
// ❌ 错误：循环变量引用（Go < 1.22）
for _, v := range items {
    go func() {
        fmt.Println(v) // v 共用底层地址
    }()
}

// ✅ 正确：先拷贝再使用
for _, v := range items {
    v := v
    go func() {
        fmt.Println(v)
    }()
}
```

### Goroutine 生命周期
```go
// ❌ 错误：goroutine 泄漏，无退出条件
func serve(ch chan string) {
    for {
        val := <-ch // 如果 ch 永不关闭，goroutine 泄漏
        handle(val)
    }
}

// ✅ 正确：明确退出条件
func serve(ctx context.Context, ch chan string) {
    for {
        select {
        case <-ctx.Done():
            return
        case val := <-ch:
            handle(val)
        }
    }
}
```

## 字面量与声明

### 空切片声明
```go
// ❌ 不推荐：非 nil 空切片
t := []string{}

// ✅ 推荐：nil 切片
var t []string

// 注意：两者 JSON 编码不同
// nil 切片 → null
// 空切片 → []
```

### 结构体字面量
```go
// ❌ 错误：外部类型未指定字段名
r := csv.Reader{',', '#', 4, false, false, false, false}

// ✅ 正确：指定字段名
r := csv.Reader{
    Comma:           ',',
    Comment:         '#',
    FieldsPerRecord: 4,
}
```

### 零值字段省略
```go
// ❌ 不必要：零值字段降低信噪比
ldb := leveldb.Open("/my/table", &db.Options{
    BlockSize:            1<<16,
    ErrorIfDBExists:      true,
    BlockRestartInterval: 0,       // 零值
    Comparer:             nil,     // 零值
    Compression:          nil,     // 零值
    WriteBufferSize:      0,       // 零值
})

// ✅ 正确：省略零值字段，突出重要配置
ldb := leveldb.Open("/my/table", &db.Options{
    BlockSize:       1<<16,
    ErrorIfDBExists: true,
})
```

### 重复类型名省略
```go
// ❌ 冗余
repetitive := []*Type{
    &Type{A: 42},
    &Type{A: 43},
}

// ✅ 正确
good := []*Type{
    {A: 42},
    {A: 43},
}
```

## 函数设计

### 缩进地狱
```go
// ❌ 错误：嵌套过深的 if 链
func GetItem(ext string) (Item, error) {
    if refIface, ok := cache.Get(ext); ok {
        if ref, ok := refIface.(string); ok {
            if itemIface, ok := itemCache.Get(ref); ok {
                if item, ok := itemIface.(Item); ok {
                    if item.Active {
                        return item, nil
                    }
                }
            }
        }
    }
    return EmptyItem, ErrNotFound
}

// ✅ 正确：提前返回，代码左对齐
func GetItem(ext string) (Item, error) {
    refIface, ok := cache.Get(ext)
    if !ok {
        return EmptyItem, ErrRefNotFound
    }
    ref, ok := refIface.(string)
    if !ok {
        return EmptyItem, ErrCastRef
    }
    return getItemByRef(ref)
}
```

### 函数参数过多
```go
// ❌ 错误：参数过多，难以理解
q, err := ch.QueueDeclare("hello", false, false, false, false, nil)

// ✅ 正确：使用 Options struct
type QueueOptions struct {
    Name         string
    Durable      bool
    DeleteOnExit bool
    Exclusive    bool
    NoWait       bool
    Arguments    []interface{}
}

q, err := ch.QueueDeclare(QueueOptions{
    Name:    "hello",
    Durable: false,
})
```

## 指针使用

### 指针作用域扩展
```go
// ❌ 错误：存储外部指针，修改会影响 store
func (s *Store) Insert(user *User) error {
    s.users[user.ID] = user // 存储外部指针
    return nil
}

func handler(w http.ResponseWriter, r *http.Request) {
    user := parseUser(r)
    store.Insert(user)
    user.Password = "" // 这会修改 store 中的数据！
    json.NewEncoder(w).Encode(user)
}

// ✅ 正确：存储副本的指针
func (s *Store) Insert(user User) error {
    s.users[user.ID] = &user // 存储副本的指针
    return nil
}
```

## 变量遮蔽

### 变量遮蔽 (Shadowing)
```go
// ❌ 错误：变量遮蔽导致意外行为
func main() {
    var val string
    switch num {
    case 32:
        val, err := doComplex() // 这里的 val 是新变量！
        if err != nil { panic(err) }
        _ = val
    }
    fmt.Println(val) // 打印空字符串
}

// ✅ 正确：提取为函数，避免遮蔽
func getResult(num int) (string, error) {
    switch num {
    case 32:
        return doComplex()
    }
    return "", nil
}

func main() {
    val, err := getResult(32)
    if err != nil { panic(err) }
    fmt.Println(val)
}
```

### 不要遮蔽标准包名
```go
// ❌ 错误：大作用域中遮蔽标准包名
func LongFunction() {
    url := "https://example.com/"
    // 之后无法使用 net/url 包
}

// ✅ 正确：使用更具体的名称
func LongFunction() {
    requestURL := "https://example.com/"
    // net/url 包仍然可用
}
```

### Context 遮蔽
```go
// ❌ 错误：条件分支中遮蔽 ctx
func (s *Server) handler(ctx context.Context, req *pb.Request) *pb.Response {
    if *shortenDeadlines {
        ctx, cancel := context.WithTimeout(ctx, 3*time.Second) // 遮蔽外层 ctx
        defer cancel()
    }
    // BUG: 这里的 ctx 仍是原始的，未设超时
}

// ✅ 正确：使用赋值而非声明
func (s *Server) handler(ctx context.Context, req *pb.Request) *pb.Response {
    if *shortenDeadlines {
        var cancel func()
        ctx, cancel = context.WithTimeout(ctx, 3*time.Second) // 赋值，不遮蔽
        defer cancel()
    }
    // ctx 现在已正确设置超时
}
```

## 空接口封装

```go
// ❌ 错误：暴露 interface{} 给用户
type HashMap struct {
    store map[string]interface{}
}

func (h *HashMap) Get(key string) (interface{}, error) {
    return h.store[key], nil
}

// 用户需要自己处理类型断言
val, _ := hashmap.Get("key")
item, ok := val.(Item) // 容易出错

// ✅ 正确：封装类型安全的 wrapper
type ItemCache struct {
    kv HashMap
}

func (c *ItemCache) Get(id string) (Item, error) {
    val, ok := c.kv.Get(id)
    if !ok {
        return EmptyItem, ErrNotFound
    }
    item, ok := val.(Item)
    if !ok {
        return EmptyItem, ErrCastFailed
    }
    return item, nil
}
```

## nil 值保护

```go
// ❌ 错误：可能访问 nil 属性
type App struct {
    Cache *KVCache
}

app := App{}
app.Cache.Add("key", "val") // panic: nil pointer

// ✅ 正确：使用 getter 保护
type App struct {
    cache *KVCache
}

func (app *App) Cache() *KVCache {
    if app.cache == nil {
        app.cache = NewKVCache()
    }
    return app.cache
}

app := App{}
app.Cache().Add("key", "val") // 安全
```

## 文档与注释

### Doc Comment 格式
```go
// ❌ 错误：不以被描述对象名称开头
// This function creates a new request
type Request struct { ... }

// ✅ 正确：以名称开头，完整句子
// Request represents a request to run a command.
type Request struct { ... }

// Encode writes the JSON encoding of req to w.
func Encode(w io.Writer, req *Request) { ... }
```

### Context 文档
```go
// ❌ 不必要：重述显而易见的 Context 取消行为
// Run executes the worker's run loop.
//
// The method will process work until the context is cancelled and accordingly
// returns an error.
func (Worker) Run(ctx context.Context) error

// ✅ 正确：Context 取消行为是隐含的
// Run executes the worker's run loop.
func (Worker) Run(ctx context.Context) error

// ✅ 正确：非标准行为需要文档
// Run executes the worker's run loop.
//
// If the context is cancelled, Run returns a nil error.
func (Worker) Run(ctx context.Context) error
```

### 并发安全文档
```go
// ❌ 不必要：只读操作天然假定并发安全
// Len returns the number of bytes.
//
// It is safe to be called concurrently by multiple goroutines.
func (*Buffer) Len() int

// ✅ 正确：需要文档的情况 - 非标准并发行为
// Lookup retrieves a value from the cache.
//
// It is safe for concurrent use by multiple goroutines.
// Note: Cache eviction may block concurrent Lookups briefly.
func (c *Cache) Lookup(key string) (value interface{}, ok bool)
```

## 测试

### 测试失败信息
```go
// ❌ 错误：信息不足
if got != want {
    t.Errorf("wrong result") // 没有上下文
}

// ✅ 正确：包含完整上下文
if got != want {
    t.Errorf("YourFunc(%q) = %d, want %d", input, got, want)
}

// ✅ 正确：使用 cmp.Diff
if diff := cmp.Diff(want, got); diff != "" {
    t.Errorf("YourFunc() mismatch (-want +got):\n%s", diff)
}
```

### 测试辅助包命名
```go
// ✅ 正确：测试辅助包命名 = 原包名 + "test"
package creditcardtest // 而非 creditcard_test 或 creditcardmock

// Stub stubs creditcard.Service and provides no behavior of its own.
type Stub struct{}
func (Stub) Charge(*creditcard.Card, money.Money) error { return nil }
```

## Panic 与 Recovery

### 内部 panic + recover 模式
```go
// ✅ 可接受：包内 panic + API 边界 recover（用于解析器等深度嵌套场景）
type syntaxError struct {
    msg string
}

func parseInt(in string) int {
    n, err := strconv.Atoi(in)
    if err != nil {
        panic(&syntaxError{"not a valid integer"})
    }
    return n
}

func Parse(in string) (_ *Node, err error) {
    defer func() {
        if p := recover(); p != nil {
            sErr, ok := p.(*syntaxError)
            if !ok {
                panic(p) // 非本包 panic，继续传播
            }
            err = fmt.Errorf("syntax error: %v", sErr.msg)
        }
    }()
    // ... 解析逻辑中调用 parseInt
}
```

### 不可达代码
```go
// ✅ 可接受：log.Fatal 后的 panic 表示不可达
func answer(i int) string {
    switch i {
    case 42:
        return "yup"
    case 54:
        return "base 13"
    default:
        log.Fatalf("Sorry, %d is not the answer.", i)
        panic("unreachable")
    }
}
```

## switch 与 break

```go
// ❌ 错误：冗余的 break
switch x {
case "A":
    doA()
    break // Go 自动 break，这是冗余的
}

// ✅ 正确：不需要 break
switch x {
case "A":
    doA()
case "B":
    // 空 case 用注释说明
}
```

## crypto/rand 使用

```go
// ❌ 错误：使用 math/rand 生成密钥
import "math/rand"
key := rand.Int()

// ✅ 正确：使用 crypto/rand
import "crypto/rand"
buf := make([]byte, 16)
if _, err := rand.Read(buf); err != nil {
    log.Fatalf("Out of randomness: %v", err)
}
key := fmt.Sprintf("%x", buf)
```

## 闭包灵活性

```go
// ❌ 不灵活：硬编码操作类型
func (d *Datastore) Do(op Operation, data []byte) error {
    switch op {
    case COMPARE:
        return d.compare(data)
    case CONCAT:
        return d.concat(data)
    }
    return ErrUnknown
}

// ✅ 灵活：使用闭包
func (d *Datastore) Do(op func([]byte) ([]byte, error)) error {
    result, err := op(d.data)
    if err != nil {
        return err
    }
    d.data = result
    return nil
}

concat := func(existing []byte) func([]byte) ([]byte, error) {
    return func(new []byte) ([]byte, error) {
        return append(existing, new...), nil
    }
}
datastore.Do(concat(newData))
```

## 带内错误（In-Band Errors）

```go
// ❌ 错误：使用特殊值表示错误
func Lookup(key string) int {
    if notFound {
        return -1 // 带内错误
    }
    return value
}

// ✅ 正确：使用多返回值
func Lookup(key string) (int, bool) {
    if notFound {
        return 0, false
    }
    return value, true
}

// ✅ 正确：返回 error
func Lookup(key string) (int, error) {
    if notFound {
        return 0, ErrNotFound
    }
    return value, nil
}
```

## 参数防御

### 导出函数 nil 校验
```go
// ❌ 错误：导出函数未校验 nil 入参
func ProcessOrder(ctx context.Context, order *Order) error {
    return s.repo.Save(ctx, order.ID, order) // order 为 nil 时 panic
}

// ✅ 正确：API 边界校验
func ProcessOrder(ctx context.Context, order *Order) error {
    if order == nil {
        return fmt.Errorf("process order: order must not be nil")
    }
    return s.repo.Save(ctx, order.ID, order)
}
```

### 指针链式访问安全
```go
// ❌ 错误：直接访问 proto 字段（非 getter），中间节点 nil 时 panic
city := req.GetUser().Address.City // GetUser() 返回 nil → .Address panic

// ❌ 错误：手写结构体链式访问未检查
city := order.User.Address.City // User 为 nil → panic

// ✅ 正确：逐层检查后访问
user := req.GetUser()
if user == nil {
    return nil, fmt.Errorf("user is required")
}
// proto getter 是 nil-safe 的，但业务上可能需要区分"未设置"和"空值"
addr := user.GetAddress()
if addr == nil {
    return nil, fmt.Errorf("user address is required")
}
city := addr.GetCity()
```

```go
// 注意：proto 生成的 GetXxx() 链式调用不会 panic，但返回零值可能导致静默错误
// ❌ 隐患：零值被当作有效数据
name := req.GetUser().GetProfile().GetDisplayName() // 不会 panic，但可能返回 ""
db.Save(&Record{Name: name}) // 空字符串被存入 DB，而非报错
```

### 内部函数信任边界
```go
// ✅ 正确：内部函数通过命名/注释表明前置条件
// processOrderInternal assumes order is non-nil and validated.
// Called only from ProcessOrder which performs validation.
func (s *service) processOrderInternal(ctx context.Context, order *Order) error {
    // 无需重复校验——调用方已保证
    return s.repo.Save(ctx, order.ID, order)
}
```

## 并发（扩展）

### time.After 泄漏
```go
// ❌ 错误：select 循环中使用 time.After（每次迭代泄漏一个 timer）
for {
    select {
    case msg := <-ch:
        handle(msg)
    case <-time.After(5 * time.Second): // 每次循环创建新 timer，旧 timer 直到触发才 GC
        log.Println("timeout")
    }
}

// ✅ 正确：使用 time.NewTimer + Reset
timer := time.NewTimer(5 * time.Second)
defer timer.Stop()
for {
    select {
    case msg := <-ch:
        if !timer.Stop() {
            <-timer.C
        }
        timer.Reset(5 * time.Second)
        handle(msg)
    case <-timer.C:
        log.Println("timeout")
        timer.Reset(5 * time.Second)
    }
}
```

### Channel 方向标注
```go
// ❌ 不推荐：无方向标注，调用方可能误操作
func consume(ch chan Event) { ... }

// ✅ 正确：编译器保证函数只能接收
func consume(ch <-chan Event) { ... }

// ✅ 正确：编译器保证函数只能发送
func produce(ch chan<- Event) { ... }
```

### 检查-然后-操作竞态
```go
// ❌ 错误：check-then-act 无锁保护
if _, ok := cache[key]; !ok {
    cache[key] = computeValue() // 两个 goroutine 可能同时进入
}

// ✅ 正确：加锁保护
mu.Lock()
if _, ok := cache[key]; !ok {
    cache[key] = computeValue()
}
mu.Unlock()

// ✅ 或使用 sync.Map.LoadOrStore
actual, loaded := cache.LoadOrStore(key, computeValue())
```
