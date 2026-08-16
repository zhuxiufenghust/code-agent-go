# pigo 30 天 Agent 开发能力训练计划

> 目标：以 pigo（Go 复刻版 pi Agent）为参照，用「手写实现」的方式逐块重做 Agent 的核心能力，
> 把"读别人的代码"变成"自己能从零写出来"。30 天结束时应具备独立设计一个 CLI 编码 Agent 的能力。
>
> 原则：
> 1. **先画接口，再写实现**——每个模块先定义 `interface` 与数据契约，再填实现。
> 2. **可测驱动**——每个功能都配一个最小可运行的冒烟测试（mock provider 即可）。
> 3. **对照不抄写**——参考 `internal/` 现有实现理解设计意图，但自己重新写出来。
> 4. **每天留 30 分钟复盘**：这个能力解决了 Agent 闭环里的哪个环节？

---

## 总体能力地图（30 天要拿下的 6 大支柱）

| 支柱 | 对应 pigo 模块 | 训练重点 |
|------|----------------|----------|
| 1. LLM 通信层 | `internal/provider` | 多协议网关、流式解析、鉴权、重试 |
| 2. Agent 主循环 | `internal/runtime/loop.go` | 请求→工具调用→结果回灌的收敛循环 |
| 3. 工具系统 | `internal/agenttool` | 工具注册、权限、执行、结构化返回 |
| 4. 会话与上下文 | `internal/session` `internal/compaction` | 续跑、压缩、上下文窗口管理 |
| 5. 扩展能力 | `internal/skills` `internal/plugin` `internal/pkgmgr` | 技能、插件、包编排 |
| 6. 工程化 | `cmd/pigo` `internal/runtime/headless.go` | 无头/REPL 双模式、输出格式、发布 |

> **harness9 生产级能力对标（重点补充）**
> 上面的 6 大支柱是"能跑"的基线。要达到 harness9 的生产可用水位，还需补齐以下核心能力；
> 其中 **Sub-Agent 委派** 与 **记忆体系（Short-Term + Long-Term）** 是本计划的重点，已融入下方 W3/W4 的训练日。
>
> | 能力 | harness9 对应模块 | 训练重点 |
> |------|-------------------|----------|
> | Sub-Agent 委派 | `internal/subagent`（`definition`/`registry`/`runner`/`task_tool`） | 子代理定义（白名单 ∩ 全集 − 黑名单 − task）、编程式 + 文件式（`.harness9/agents/*.md`）注册、隔离子引擎、前台/后台双模式、`TaskTracker`、防递归 + 权限不扩权 + 上下文隔离 |
> | 长期记忆 LTM | `internal/ltm`（`store`/`precis`/`extractor`） | `Store`（`long_term_memories` + standalone FTS5 `memories_fts`，复用 `state.db`，SHA256 签名去重 / TTL / 软删除 / 命中强化）、`Precis`（MEMORY.md 物化视图）、`Extractor`（压缩前 LLM 提取，fail-open）、三路触发（memory_write/memory_search 工具 + 压缩前 Extractor + `WithMemoryNudge`） |
> | 短期记忆 | `internal/memory`（`session`/`manager`/`compaction`/`summarization`） | `Session`/`Manager`（SQLite CRUD + 级联 GC）、`SummarizationCompactor`（默认 LLM 摘要 + 增量更新 + 回退）、`TokenBudgetCompactor`/`SlidingWindowCompactor`、80% 阈值双向修复孤立工具对、token 估算 |
> | Planning / Todo | `internal/planning`（`mode`/`todo`/`plan_writer`） | `PlanMode` 枚举、跨会话持久化 `TodoStore`（Session 层 `SaveTodos`/`RestoreTodos`）、`FilePlanWriter` |
> | Skills | `internal/skills` | 渐进式披露、YAML frontmatter、`Index`、`UseSkillTool` 按需加载 |
> | Sandbox | `internal/sandbox` | Docker 容器级隔离、`Environment` 接口、`Container` 五状态机、`Manager`、工具透明路由、安全加固（cap-drop/no-new-privileges/pids-limit）、Agent 级隔离 |
> | MCP | `internal/mcp` | JSON-RPC 2.0、`StdioTransport`/`HTTPTransport`、`Client` 握手（initialize→initialized→tools/list）、`Manager`、`MCPToolAdapter` |
> | HITL 权限 | `internal/hooks`（`decision`/`danger_hook`） | `HookDecision`（allow/deny/ask）、`DangerHook`、`PermissionHook`、敏感路径硬保护、审批对话框、`PermissionMode` |
> | 可观测 | `internal/observability` | OTEL `OTELEngineObserver`/`TracingProvider`/`ObservabilityHook`、Span 嵌套、Metrics、Exporter（noop/stdout/otlp） |
> | 网页检索 | `internal/tools`（`web_search`/`web_fetch`/`web_safety`） | DuckDuckGo 搜索、HTML→Markdown、`isSafeURL` SSRF 防护、当前日期注入 |

---

## 第 1 周（Day 1–7）：打通 LLM 通信与最小 Agent 闭环

**本周目标**：能用 Go 向一个大模型发请求、解析流式响应，并跑通"模型想调工具 → 我执行 → 结果回灌"的最小循环。

- **Day 1**　环境搭建：Go module 初始化、项目骨架（cmd / internal 分层）、`go.mod`、第一个 `main` 打印版本。
- **Day 2**　Provider 接口设计：定义 `Provider` 接口（`Chat`, `Stream`），统一 `Message`/`ToolCall` 数据模型。
- **Day 3**　OpenAI 协议实现：请求体拼装、SSE 流式解析、工具调用（function calling）字段。
- **Day 4**　Anthropic 协议实现：Messages API 结构差异、流式 event 解析、tool_use 块。
- **Day 5**　Provider 注册表与模型 id 启发式路由（`openrouter/` `ollama/` `anthropic/` 前缀推断）。
- **Day 6**　鉴权与配置：Key 解析顺序（oauth → flag → env → 配置文件），base_url 覆盖优先级。
- **Day 7**　**周成果**：`pigo -p "1+1=?"` 无头打印能返回答案；用 mock provider 写 3 个测试。

> 能力收获：理解"模型不是函数调用，是带状态的状态机"。

---

## 第 2 周（Day 8–14）：工具系统与 Agent 主循环

**本周目标**：实现工具注册表 + 一组文件/命令工具，并让主循环能真正"用工具改代码"。

- **Day 8**　工具抽象：定义 `Tool` 接口（`Name`/`Description`/`InputSchema`/`Run`），JSON Schema 描述入参。
- **Day 9**　`read` / `write` / `edit`：带行号输出、超大文件截断、`old_string` 唯一性校验 + diff 返回。
- **Day 10**　`grep` / `find`：正则检索、glob 过滤、跳过 `.gitignore` 路径。
- **Day 11**　`bash` 工具：流式 stdout/stderr、超时与取消（context）、副作用分级。
- **Day 12**　Agent 主循环 `loop.go`：把模型响应里的 tool_calls 分发到工具，结果回填 messages，直到 `stop_reason=end`。
- **Day 13**　`todo` 工具 + 结构化任务清单（pending/in_progress/completed）的整表提交。
- **Day 14**　**周成果**：`pigo -p "把 utils.go 里的 getUserName 重命名为 getUsername"` 能真实改文件；本周工具各带测试。

> 能力收获：主循环是 Agent 的"心脏"，收敛条件（何时停）比执行更重要。

---

## 第 3 周（Day 15–21）：会话、上下文与记忆体系

**本周目标**：让 Agent 有记忆、能续跑、能压缩上下文，并掌握 harness9 双层记忆体系（Short-Term 会话 + Long-Term 跨会话），学会"组装提示词"。

- **Day 15**　会话模型：定义 `Session`、JSONL/SQLite 事件存储、`--list-sessions` / `--resume` / `--continue`；会话级 `cancel` 传播。
- **Day 16**　会话序列化与回灌：历史 messages 还原成可续跑状态；`Manager`（SQLite CRUD + `DeleteSession` 级联 GC）。
- **Day 17**　系统提示词分层组装：base 指令 + 环境块（cwd/OS/日期）+ `AGENTS.md` 由通用到具体拼接；`DefaultPromptBuilder` 每轮注入"当前日期"（防陈旧搜索词）。
- **Day 18**　上下文压缩（Short-Term）：`SummarizationCompactor`（LLM 摘要 + 增量更新 + 错误回退），80% 阈值触发、双向修复孤立工具对；回退 `TokenBudgetCompactor` / `SlidingWindowCompactor`；token 估算（字符数 ÷ 4）。
- **Day 19**　长期记忆 LTM·存储层：复用 SQLite 连接建 `long_term_memories` 表 + standalone FTS5 `memories_fts`；SHA256 内容签名去重、TTL 过期、软删除（`signature=NULL` 释放槽位）、命中强化（`use_count`/`last_used_at`）、`StaleCandidates`。
- **Day 20**　长期记忆 LTM·物化与提取：`Precis`（MEMORY.md 物化视图，top-N + 5KB 截断，每轮重读注入）；`Extractor`（压缩前 LLM 提取持久事实，fail-open）；三路触发（memory_write/memory_search 工具 + 压缩前 `Extractor` + `WithMemoryNudge` 防停滞）。
- **Day 21**　**周成果**：长对话自动压缩不丢关键信息；一次 `--continue` 无损续跑；新写入的 LTM 经 `Precis` 进入下轮提示、可被 `memory_search` 检索；本周模块带测试。

> 能力收获：记忆体系是 Agent 的"第二大脑"——短期记忆管当下上下文，长期记忆管跨会话沉淀；上下文工程是 Agent 质量的上限，比模型本身更可控。

---

## 第 4 周（Day 22–30）：扩展能力、Sub-Agent、双模式与工程化

**本周目标**：补齐技能/插件/包管理，落地 harness9 式 **Sub-Agent 委派系统**（重点），接入 MCP / Sandbox / HITL / Observability，做无头+REPL 双形态，收尾为可发布的 CLI。

- **Day 22**　技能系统 `skills.go`：YAML frontmatter 解析、目录发现（扁平 + 嵌套）、`/skill-name` 暴露、`UseSkillTool` 渐进式按需加载（避免启动整体注入上下文）。
- **Day 23**　斜杠命令 `slashcommand.go`：内置命令（`/model` `/models` `/help` `/compact` `/tree` 等）分发。
- **Day 24**　**Sub-Agent 系统·定义与注册（重点）**：定义 `SubAgentDefinition`（`ResolveTools` = 白名单 ∩ 全集 − 黑名单 − task）；`Registry` 双轨注册（编程式 + 文件式 `.harness9/agents/*.md` YAML frontmatter，文件定义可覆盖同名内置）；内置 `general-purpose` 通用子代理（继承父全部可用工具与模型）。
- **Day 25**　**Sub-Agent 系统·运行与委派（重点）**：`Runner` 构建隔离子引擎（`RunStream` + 独立 `Session`/prompt/compactor）+ 进度/审批桥接 + 从会话级 `baseCtx` 派生 `execCtx` 绕过父 60s 工具超时；`TaskTool` 前台/后台双模式；`TaskTracker` 后台任务单一事实源；纵深防御禁止递归委派（task 永不进子代理工具集）+ 权限只能更严 + 上下文完全隔离。
- **Day 26**　MCP 集成 `mcp.go`：JSON-RPC 2.0 over stdio/HTTP；`StdioTransport`（subprocess + NDJSON 异步读 + pending map 关联）+ `Client` 握手（initialize→notifications/initialized→tools/list）+ `Manager`（并发连接 fail-soft、30s/服务器超时）+ `MCPToolAdapter`（命名 `mcp__{server}__{tool}`，对引擎透明）。
- **Day 27**　Sandbox 与 HITL：`DockerEnvironment`/`LocalEnvironment`（`Environment` 接口）、`Container` 五状态机、`Manager`、工具透明路由（bash via docker exec，文件 via bind mount）、安全加固（cap-drop all + no-new-privileges + pids-limit + tmpfs）、Agent 级容器隔离；`HookDecision`（allow/deny/ask）+ `DangerHook`（19 条高危模式）+ `PermissionHook` + 敏感路径硬保护（~/.ssh 等）+ 审批对话框。
- **Day 28**　可观测与输出格式：`OTELEngineObserver`（Interaction/Turn Span）/ `TracingProvider`（LLM Span + Token Metrics）/ `ObservabilityHook`（Tool Span），Exporter（noop/stdout/otlp）；输出 text 与 `stream-json`（逐行 JSON 事件）。
- **Day 29**　REPL 交互 + 项目信任 `trust`：终端检测（TTY→TUI / 管道→CLI REPL）、逐 token 渲染、交互式工具确认；Trusted/Untrusted/Undecided 三态持久化、`--approve` 会话级授权。
- **Day 30**　**结营成果**：完整 `pigo` 二进制（含 **Sub-Agent 委派** + **LTM 长期记忆** + MCP + Sandbox + HITL + OTEL）；用 `goreleaser` 跑一次 snapshot 构建；写"我从零实现 Agent 学到了什么"复盘文档。

> 能力收获：扩展机制（Sub-Agent / Skills / MCP）决定 Agent 能否从"玩具"长成"平台"；记忆与隔离决定它能否安全长跑。

---

## 每周交付清单（自检用）

| 周次 | 必须能跑通的命令 | 必须存在的测试 |
|------|------------------|----------------|
| W1 | `pigo -p "1+1=?"` | provider 路由 + 流式解析（mock） |
| W2 | 改文件类 prompt 真实落地 | read/write/edit/grep/bash/todo |
| W3 | `--continue` 续跑 + 自动压缩 + LTM 写入/检索 | session 序列化 + compaction + LTM Store/FTS5/Precis |
| W4 | REPL + Sub-Agent 委派 + MCP + Sandbox + stream-json | skills/subagent/mcp/sandbox/hitl 关键路径 |

---

## 学习节奏建议

- 每天 2–3 小时，先 30 分钟读 pigo 对应模块理解设计，再用 1.5 小时手写自己的版本。
- 卡住时回到 pigo 源码对照，但**不要整段复制**，只借用思路。
- 每完成一个支柱，在 `docs/` 下留一篇短笔记（设计取舍 + 踩坑），30 天后即是一本"自己写的 Agent 开发手册"。
