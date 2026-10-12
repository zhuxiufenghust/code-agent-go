---
name: review
description: "Go 代码审查专家，基于 Go 官方规范、Google Go 风格指南和富途后端编码规范，对代码变更进行系统性审查并输出分级报告（Critical/Important/Style）。支持审查本地文件、Git diff、分支变更和 GitLab MR。当用户提到以下任何场景时必须触发：代码审查、review、CR、代码走查、帮我看看代码、帮我检查下、这段代码有问题吗、这样写行不行、检查代码质量、看看有没有 bug、review code、check PR、code review、look at my changes、check my code、merge request review。即使用户只是随口说'帮我看看'或'检查一下'，只要上下文涉及 Go 代码，也应触发此 skill。"
argument-hint: "[file-path | branch | MR-number]"
allowed-tools: Read, Grep, Glob, Bash(git diff *), Bash(git log *), Bash(git show *), Bash(git branch *), Bash(go vet *), Bash(go build *), Bash(go test *), Bash(gofumpt *), Bash(gofmt *), Bash(golangci-lint *), Bash(staticcheck *), mcp__gitlab__get_merge_request, mcp__gitlab__get_merge_request_diffs, mcp__gitlab__list_merge_request_diffs, mcp__gitlab__mr_discussions, mcp__gitlab__create_merge_request_thread, mcp__gitlab__create_merge_request_note, mcp__gitlab__create_draft_note, mcp__gitlab__bulk_publish_draft_notes
---

# Go Code Review

代码审查的核心目标：帮助团队交付可靠、可维护的代码。站在**读者**的视角，判断代码是否清晰传达了意图。

## 审查原则

**质量优先级**：Clarity > Simplicity > Concision > Maintainability > Consistency

这个排序很重要——当清晰性和简洁性冲突时，选择更清晰的写法。一段稍长但意图明确的代码，胜过一段精巧但需要反复阅读才能理解的代码。

**增量审查**：只审查变更的代码。对未改动的已有代码提建议是浪费双方时间，除非变更引入了与周围代码的不一致。

**诚实审查**：只报告有确切证据的问题。对于不确定的问题，用 `[question]` 标注并请作者确认，而不是假定为 bug。猜测性的 review 会消耗信任。

**沟通**：使用与用户相同的语言（中文提问则中文回复）。代码审查是协作而非批评——在指出问题时也肯定优秀设计。

## 审查流程

### Step 1: 确定审查范围

根据 $ARGUMENTS 判断输入来源：

```
纯数字 → MR 编号 → 走 MR 审查流程（见末尾）
文件路径存在 → 直接读取指定文件/目录
git branch -a 匹配 → git diff main...{branch}
无参数 → git diff + git diff --cached
```

**效率约束**：变更文件 + 上下文文件控制在 15 个以内。超出时优先审查核心业务逻辑，跳过纯机械性变更（如 mock 文件、自动生成代码）。

### Step 2: 理解变更意图

在逐行审查之前，先理解"这次变更要做什么"——不理解意图就无法区分"有意的设计选择"和"无意的错误"。

1. 通过 MR 描述 / commit message / 询问用户，明确变更目的
2. 读取变更涉及的相关文件，理解调用链和依赖关系
3. 根据变更类型调整侧重：新功能关注 API 设计和可扩展性，Bug 修复关注完整性和回归风险，重构关注行为一致性

### Step 3: 执行审查

根据变更规模选择策略。

| 规模 | 策略 |
|------|------|
| < 50 行 | 逐行精细审查 |
| 50-300 行 | 标准审查，按严重程度优先级检查 |
| > 300 行 | 先给架构反馈，再审查关键路径，Style 级别只报模式性问题 |

#### Step 3a：工具扫描（确定性）

按优先级尝试执行，不可用时自动 fallback：

| 类别 | 优先级 1 | 优先级 2 | 优先级 3 |
|------|---------|----------|----------|
| 格式化 | `gofumpt -l` / `gofumpt -d` | `gofmt -l` / `gofmt -d` | — |
| 静态分析 | `golangci-lint run` | `staticcheck` | `go vet` |
| 编译（必做） | `go build ./...`（或变更包） | — | — |
| 竞态（并发相关变更必做） | `go test -race ./changed-pkgs` | `go vet`（基础) | — |

**编译与竞态说明**：
- `go build` 用于过滤"连编译都不过"的低级错误，先做这一步，免得把 AI 审查花在语法/类型问题上
- **只要变更涉及 goroutine / channel / sync / 共享状态 / 并发写**，必须跑 `go test -race`，并记录输出。`-race` 只能覆盖被测试执行到的路径，因此即便 `-race` 通过，AI 仍要按 checklist 检查"有无可以证明正确的同步原语"
- 没有对应测试时记录 `[tool-skipped: 无用例可跑 -race]`，并在报告里建议补充并发用例（见 checklist 测试规范）

**工具执行范围**：仅扫描本次变更涉及的文件/包（与 Step 1 确定的审查范围一致），不扫描整个仓库。对于 golangci-lint，使用 `--new-from-rev=<base>` 或指定变更文件路径来限定范围。

**退出码判定规则**（决定使用输出还是 fallback）：

| 情况 | 判定 | 动作 |
|------|------|------|
| 命令不存在（command not found） | 工具不可用 | fallback 到下一优先级 |
| 退出码 0，无输出 | 检查通过 | 记录 "✅ 无问题" |
| 退出码 0 或 1，有输出 | 发现问题 | **使用输出**，不 fallback |
| 退出码 ≥ 2 或单次调用超时（30s） | 工具异常 | 记录 `[tool-error: xxx]`，fallback |

**关键**：`golangci-lint run` 发现 lint 问题时返回退出码 1，这是**正常行为**而非失败，必须使用其输出而非 fallback。

其他规则：

- 工具输出**原样记录**，AI 不做二次解读或补充
- 所有工具均不可用时记录 `[tool-unavailable: xxx]`，**不由 AI fallback 检查**
- 工具输出超过 50 行时，保留前 50 行并附 `... 省略 N 行，完整输出请本地运行工具查看`

**MR 审查场景**：当审查来源为 GitLab MR 且无本地代码 checkout 时，跳过 Step 3a，记录 `[tool-skipped: no local checkout]`。仅通过 Step 3b 的 AI 审查进行，此时 AI **不**额外承担格式/lint 检查（保持职责边界一致）。

#### Step 3b：AI 审查（语义性）

AI 只审查工具无法覆盖的项目。明确排除范围：

- 代码格式化问题（gofumpt/gofmt 职责）
- golangci-lint 默认规则集已覆盖的检查（errcheck、govet、unused、gosimple、ineffassign、staticcheck 等）

AI 专注的领域不变：架构合理性、并发安全、业务逻辑正确性、命名语义、错误处理策略、Kafka 状态管理等。

审查分两个阶段思考：

**先看全局**——从架构和数据流角度审视，这些系统性问题往往比单个命名规范影响更大：

- **追踪返回值到调用方**：当函数返回多个值（如 `(skip bool, result T, err error)`），仅看函数本身不够，必须阅读调用方代码确认每种组合都被正确处理。这是因为"函数返回正确"和"调用方使用正确"是两件不同的事，而 bug 往往出在这个衔接处。
- **多步操作原子性**：write → produce → update 等序列中，中间步骤失败后系统是否处于不一致状态？有无补偿机制？
- **配置/共享状态一致性**：函数体内多次调用 `cfg.Get()` 且服务支持热重载时，两次调用间可能看到不同配置快照。
- **热路径性能**：对于高频调用路径（流式数据处理、行情推送、批量接口等），检查是否有不必要的 I/O（如 INFO 日志）、可避免的分配（如循环内 `fmt.Sprintf`）。
- **错误路径的状态完整性**：当 "操作 A → 操作 B → 标记完成" 是一个序列时，如果操作 B 失败但仍然标记完成，数据会不可恢复地丢失。检查错误分支是否正确保留了中间状态以便重试。
- **外部调用超时**：每个 HTTP、gRPC、DB 调用是否有显式超时（通过 context 或 client 配置）？"无超时"等于"无限超时"，最终导致资源耗尽。
- **优雅关闭顺序**：停止接收新请求 → 等待进行中请求完成（带超时） → 释放资源。检查 shutdown handler 是否遗漏了某些需要 drain 的 goroutine 或 channel。

当审查涉及 Kafka Consumer、Rebalance、消息序号跟踪等场景时，还需关注以下领域特定模式：

- **Upsert 幂等性（高风险）**：`ON DUPLICATE KEY UPDATE col = VALUES(col)` 是无条件覆盖——当多个实例并发写同一行时，后写者胜出而非大者胜出。如果被更新的字段是单调递增的序号/进度/版本号，必须使用 `GREATEST(col, VALUES(col))` 保护，否则延迟写入的旧实例可能将 DB 中的值**回退**到较小值。这类问题在 Kafka Rebalance 场景下尤其致命：旧 Consumer 实例的 Producer pipeline drain 写入可能覆盖新实例已经写入的更高值。
- **Consumer/Producer pipeline 状态断层**：当系统中 Consumer 侧（内存）和 Producer 侧（DB）各自维护同一状态的副本、但更新时机不同步时（如 Consumer 即时推进 seq，Producer 通过定时器延迟刷 DB），Rebalance 后新实例从 DB 读取到的是滞后状态。检查：(1) 定时刷新间隔内发生 Rebalance 是否导致状态丢失；(2) Cleanup/Destroy 回调是否主动触发最后一次 flush。
- **阻塞式清理引发级联故障**：`Destroy()` / `Close()` 等待长耗时操作（如 GapFill 逐条推送到无缓冲 channel）完成，可能超过 Kafka `session.timeout.ms`，触发 Broker 级联 Rebalance。

**再看细节**——按严重程度分类。完整检查项参见 [references/checklist.md](references/checklist.md)，按需查阅对应章节即可。

#### 严重程度分类

判断标准用这个决策树：

```
这段代码上线后会导致数据损坏、安全事故或服务不可用吗？
├── 是 → 🔴 Critical
└── 否 → 这段代码会让下一个读者困惑，或在 6 个月后埋坑吗？
    ├── 是 → 🟡 Important
    └── 否 → 🟢 Style
```

#### 🔴 Critical — 必须修复

会导致 bug、安全漏洞或运行时崩溃。常见类型：

- **安全漏洞**：硬编码密钥、SQL 注入、`math/rand` 用于密钥生成、**命令注入（外部输入进 `sh -c`）、路径穿越/符号链接逃逸、SSRF（未拦截内网/元数据地址）、关闭 TLS 校验**
- **panic 风险**：业务逻辑中 panic、未保护的切片/map 索引、**goroutine 内的 panic 无 recover（拖垮整个进程）**、**nil map 写入 / 重复 close channel**
- **资源泄漏**：`resp.Body`、`sql.Rows`、文件句柄未 Close；`WithCancel/WithTimeout` 未调用 cancel；`time.NewTicker` 未 Stop；循环体内 `defer`；无上限的全局缓存
- **goroutine / channel 泄漏**：起了 goroutine 却没有任何退出路径；向无人接收的 channel 发送导致永久阻塞；上游 context 取消没有向下传播；收尾写库复用了已被取消的 ctx（应使用 `context.WithoutCancel`）
- **错误吞没**：`_` 忽略关键 error 且无注释；error 仅 log 但不返回，导致脏数据继续写入；**defer 里 recover 后静默吞掉栈**
- **返回值契约违反**：函数返回矛盾信号（如同时 `skip=true, err!=nil`），导致调用方丢弃错误。审查这类问题时必须阅读调用方代码——不能只看被调函数本身
- **GORM 陷阱**：`Save()` 或结构体默认更新（零值覆盖有效数据）
- **并发问题**：无限制开 goroutine、sync 类型值传递
- **隐蔽副作用**：函数名暗示查询/转换但实际通过指针修改了输入参数，在后续失败场景下导致内存与 DB 状态不一致
- **Upsert 序号/进度回退**：`ON DUPLICATE KEY UPDATE seq = VALUES(seq)` 无条件覆盖单调递增字段——多实例并发写入时延迟的旧值可覆盖新值，导致 DB 中的 seq 回退。修复：`GREATEST(seq, VALUES(seq))`
- **错误路径资源泄漏**：DB 批写失败后仍清空内存缓存并标记已处理（推进 seq），导致数据不可恢复丢失。错误分支必须保留未持久化的数据以便重试
- **阻塞式清理引发级联故障**：`Destroy()` / `Close()` 等待长耗时操作（如 GapFill 逐条推送到无缓冲 channel）完成，可能超过 Kafka `session.timeout.ms`，触发 Broker 级联 Rebalance

#### 🟡 Important — 强烈建议修复

不会立即出 bug，但增加未来出问题的概率：

- 命名不规范（snake_case、缩写词大小写不一致、命名冗余）
- 错误处理模式问题（大写 error 字符串、无用包装、日志重复）
- Context 使用不当（不是第一个参数、存在结构体中）
- 接口定义在实现方而非使用方——这会导致实现方和使用方产生不必要的耦合，使用方无法只依赖自己需要的方法子集
- 外部调用缺少超时
- Error 日志未伴随 fmonitor 上报
- 正常流程缩进在 else 中（错误处理应前置，保持正常路径左对齐）

#### 🟢 Style — 建议优化

不影响正确性但影响代码整洁度。大型变更只报模式性问题，不逐行纠正格式。

典型项：Doc Comment 格式、可省略的重复类型名/零值字段、冗余 break、作用域内过长变量名。

### Step 4: 输出审查报告

每个问题要做到三点：**定位**到具体文件和行号，**解释**为什么是问题（会导致什么后果），**提供**具体修复代码。

同时肯定优秀的设计选择——说明好在哪里，帮助团队建立共识。

**报告格式：**

````markdown
## 代码审查报告

> **变更概要**: [一句话描述变更目的和范围]
> **审查策略**: [小型精细审查 / 标准审查 / 大型分层审查]

### 🔧 工具扫描结果

**格式化** (`gofumpt -d` / `gofmt -d`):
```
工具原始输出，或 "✅ 无格式问题"，或 "[tool-unavailable: gofumpt, gofmt]"
```

**静态分析** (`golangci-lint run` / `staticcheck` / `go vet`):
```
工具原始输出，或 "✅ 无 lint 问题"，或 "[tool-unavailable: golangci-lint, staticcheck, go vet]"
```

> 以上为确定性工具的原始输出，不含 AI 解读。如需修复，直接按工具提示操作。
> 使用的具体工具会在括号中标注。如显示 `go vet`，说明 golangci-lint 和 staticcheck 均不可用，覆盖范围有限。

---

### 🔴 必须修复 (Critical)

**1. [问题标题]** — `path/file.go:123`

问题描述：解释为什么严重，会导致什么后果。

```go
// 当前代码
problematic code here
```

建议修复：

```go
// 修复后
fixed code here
```

---

### 🟡 建议修复 (Important)

**1. [问题标题]** — `path/file.go:456`

问题描述和修复建议（同上格式）。

---

### 🟢 可优化项 (Style)

- `path/file.go:789` — 简要说明和建议

---

### ✅ 优秀实践

- 列出代码中值得肯定的设计选择，说明好在哪里

### 📊 审查总结

- **总体评价**：[概括性评价]
- **问题统计**：Critical: N / Important: N / Style: N
- **关键改进建议**：[最重要的 1-2 项改进方向]
````

> 如果审查范围内包含 `_test.go` 文件，在总结前增加**测试评估**部分：关键路径覆盖度、错误路径和边界条件是否覆盖。

### Step 5: 自检（防止漏查）

报告输出前，逐条问自己这四个问题，答不上来的要回去看代码：

1. **每个新起的 goroutine 都有退出路径吗？** 它的退出条件、由谁负责、上游取消能否走到它那里
2. **每个新申请的资源都有释放点吗？** Body / Rows / File / Conn / cancel() / ticker.Stop()
3. **panic 边界清楚吗？** 导出的函数会 panic 吗？goroutine 里有 recover 吗？recover 后有栈吗？
4. **外部输入进了哪些"危险槽位"？** 命令参数、文件路径、URL、SQL、日志 —— 逐个确认有校验/转义/白名单

任一答案为"不确定"，就回到 Step 3b 补查，而不是假设没问题。

## MR 审查流程

当 $ARGUMENTS 是 MR 编号时，在标准审查流程基础上增加：

### 1. 获取上下文
- `mcp__gitlab__get_merge_request` 获取 MR 信息（标题、描述、作者）
- `mcp__gitlab__get_merge_request_diffs` 获取完整代码变更
- `mcp__gitlab__mr_discussions` 查看已有讨论，避免重复评论

### 2. 检查元信息
- MR 描述是否说明了变更目的、背景、影响范围
- Commit Messages 是否符合 `type(scope): description` 规范

### 3. 执行标准审查
按 Step 2-4 执行。

### 4. 发布评论

评论按严重程度标注前缀：
- **[blocking]** → Critical，必须修复才能合并
- **[suggestion]** → Important，强烈建议修复
- **[nit]** → Style，不阻塞合并
- **[question]** → 需要作者解释意图

操作：
1. 每个问题用 `mcp__gitlab__create_draft_note` 创建草稿评论
2. 全部就绪后 `mcp__gitlab__bulk_publish_draft_notes` 批量发布
3. 用 `mcp__gitlab__create_merge_request_note` 发布审查总结

## 参考资料

> 本 SKILL.md 已覆盖大部分常见审查规则。参考文件仅在"拿不准"时查阅——在绝大多数审查中你不需要它们。

### 详细审查清单
→ [references/checklist.md](references/checklist.md)
**何时查阅**：遇到不常见的主题（如泛型、Receiver 选择）、需要确认某条具体规则时。查阅对应章节即可。

### 代码正反示例
→ [REFERENCE.md](REFERENCE.md)
**何时查阅**：需确认某个写法是否为已知反模式、需要给修复建议提供正反对照时。

### Skill 自测
→ [SELFTEST.md](SELFTEST.md)
**何时使用**：修改了本 skill（SKILL.md / checklist / REFERENCE）后、或首次安装到新环境时，验证 skill 能被发现、能触发、能覆盖埋好的缺陷。`fixtures/` 下是专门埋了缺陷的样本，`fixtures/EXPECTED.md` 是答案。
