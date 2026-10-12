# `fixtures/` 基线答案

`fixtures/` 下每个文件都刻意埋了缺陷。这些条目是 **review skill 的召回率基线**：跑一次审查后逐项对照，
命中率应 ≥ 80%，且所有 "AI 必查" 条目不得整类漏报（例如一个并发泄漏都没报 = 流程失效）。

列中的"归属"说明该缺陷应该被谁发现：

- **工具**：`gofumpt/gofmt`、`golangci-lint`/`staticcheck`/`go vet`、`go build`、`go test -race` 能确定性发现
- **AI**：工具覆盖不了，必须由 Step 3b 语义审查发现

> 注意：本机当前只装了 `go build`/`gofmt`/`go vet`（`golangci-lint`、`staticcheck`、`gofumpt` 缺失），
> 所以标记为"工具"的条目在没有对应工具时会退化成 AI 必查。测试时应以"实际可用工具"为准。

---

## 1. concurrency_leak.go

| # | 位置（`FuncName`） | 缺陷 | 严重度 | 归属 |
|---|---|---|---|---|
| 1 | `Snapshot` | 值接收者复制了含 `sync.Mutex` 的结构体（锁语义失效） | 🔴 | 工具（`go vet` copylocks）/ AI |
| 2 | `RunHandlers` | 无并发上限地 `go func()`，item 数量即 goroutine 数量 | 🔴 | AI |
| 3 | `RunHandlers` | goroutine 向无缓冲 chan 发送，无 `<-ctx.Done()` 兜底，接收方退出即永久阻塞泄漏 | 🔴 | AI |
| 4 | `RunHandlers` | `out` 被 goroutine 追加、主协程同时返回读取 —— 数据竞争 | 🔴 | AI / `-race`（有用例时） |
| 5 | `RunHandlers` | 用 `time.Sleep` 充当同步，"等一会"不是 happens-before | 🔴 | AI |
| 6 | `RunHandlers` | 聚合 goroutine 的 `for range ch` 无人 close channel —— 永不退出 | 🔴 | AI |
| 7 | `Watch` | `time.NewTicker` 没有 `ticker.Stop()`（每次调用泄漏一个 timer） | 🔴 | AI |
| 8 | `Watch` | 起 goroutine 却不返回任何句柄/也不接受 ctx —— 调用方无法等待或取消 | 🟡 | AI |
| 9 | `WithTimeoutOnce` | `cancel` 被 `_ = cancel` 吃掉，父 ctx 与 timer 泄漏 | 🔴 | AI（`golangci-lint` lostcancel 可抓） |
| 10 | `Collect` | `done` 未初始化（nil channel），发送永久阻塞 —— 死锁 | 🔴 | AI |
| 11 | `Collect` | 多个 goroutine 无保护写 `totals` map —— 并发写 map 恐慌风险 | 🔴 | AI / `-race` |
| 12 | `Collect` | map 遍历计数不可靠（goroutine 完成顺序不确定），同一性假设错误 | 🟡 | AI |

## 2. security.go

| # | 位置 | 缺陷 | 严重度 | 归属 |
|---|---|---|---|---|
| 13 | `apiToken` | 硬编码密钥（且是 `sk-live-*` 形态的活跃凭据形态） | 🔴 | AI（`gosec`/gitleaks 可抓） |
| 14 | `RunScript` | 外部输入拼进 `sh -c` —— 命令注入 | 🔴 | AI（`gosec` G204 可抓） |
| 15 | `ReadDoc` | `filepath.Join` 不做前缀校验，用户传入 `../` 可读任意文件 —— 路径穿越 | 🔴 | AI |
| 16 | `FetchURL` | 直连用户提供的 URL，未拦截内网/回环/链路元数据地址 —— SSRF | 🔴 | AI |
| 17 | `FetchURL` | `InsecureSkipVerify: true` 关闭 TLS 校验 —— 中间人风险 | 🔴 | AI（`gosec` G402 可抓） |
| 18 | `FetchURL` | `http.Client` 无 Timeout；也无限流/大小限制（超大响应撑爆内存） | 🟡 | AI |
| 19 | `FetchURL` | 出错返回空串丢失失败信息（错误吞没），且 `io.ReadAll` 未限制大小 | 🟡 | AI |
| 20 | `LookupUser` | SQL 字符串拼接 —— SQL 注入 | 🔴 | AI（`gosec` G201 可抓） |
| 21 | `LookupUser` | `SELECT *` 且返回 `*sql.Row` 让调用方延迟感知错误 — API 设计问题 | 🟢 | AI |
| 22 | `NewRequestID` | 用 `math/rand` 生成标识（可预测），应选 `crypto/rand` | 🔴 | AI（`gosec` G404 可抓） |

## 3. errors_panic.go

| # | 位置 | 缺陷 | 严重度 | 归属 |
|---|---|---|---|---|
| 23 | `LoadConfig` | 出错仅打日志并返回 nil —— 调用方无感，后续 nil 解引用 | 🔴 | AI |
| 24 | `LoadConfig` | `json.Unmarshal` 的错误被丢弃（下划线/无 errcheck） | 🔴 | AI（errcheck 可抓） |
| 25 | `MustPort` | 导出函数在业务支路上 panic，panic 跨越包边界 | 🔴 | AI |
| 26 | `MustPort` | `Must*` 命名暗示失败必 panic，但接收的是可被外部构造的配置对象 | 🟡 | AI |
| 27 | `Reload` | goroutine 内没有 `defer recover()`，其 panic 会拖垮整个进程 | 🔴 | AI |
| 28 | `Reload` | 异步重载无 ctx/取消，也无"是否完成"的可见性 | 🟡 | AI |
| 29 | `CountBySize` | `var counts map[int64]int` 未初始化即写入 —— nil map 写 panic | 🔴 | AI |
| 30 | `Save` | 文件 `f` 从未 Close（成功路径也泄漏 fd），应为 `defer f.Close()` + 关闭错误检查 | 🔴 | AI |

---

## 反向对照（确保不误报）

以下写法是**误报红线**——它们看起来可疑但实际合规，报告中若把它们列为问题即计为误报：

- `Must*` 前缀的初始化函数内部 panic（命名已告知调用方契约）
- 参数化合法的 `exec.Command(name, args...)`（每个参数独立传入，无 shell 解释）
- 忽略返回值但附了明确理由注释的场景（如 `_ = cancel // 由 ctx 超时自动回收`）
- 已经用 `errcheck` 或注释确认的场景下对 `_ = xxx` 的重复指控

---

## 评分

```
召回率 = (命中的条目数 ÷ 30) × 100%，目标 ≥ 80%
```

同时记录三项数据便于横向比较：

- **工具命中数 vs AI 命中数**：工具命中占比过低说明没有正确执行 Step 3a
- **误报条目数**：对照上面的红线清单 + 明显无中生有的问题
- **同类漏报**：某一整类（如 4 个并发泄漏条目）全部未报 = 流程失效，即使总召回率达标也要重跑
