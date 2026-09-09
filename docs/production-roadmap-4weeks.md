# code-agent-go 生产可用化差距分析与 4 周实施计划

> 目标：在目前已经跑通「LLM 返回 → 解析 tool_calls → 执行工具 → 结果回灌」最小闭环的基础上，
> 逐步补齐一个**生产级编码 Agent** 必备的能力。本文与 `docs/30-days-plan.md`（手写训练计划）互补：
> 那份关注"从零学会实现"，这份关注"从能跑到能用"。
>
> 现状基线（截至本计划起草时）已具备：
> - OpenAI 兼容 Provider（流式 + 非流式），`GenerateStream` 已正确下发 `tools` 并解析原生 `tool_calls`；
> - 工具系统：read / write / edit / bash / web_search / web_fetch / skill_dispatcher，统一 `Tool` 接口与注册表；
> - Agent 主循环 `runLoop` 支持多轮 tool call、并发执行、重试退避、结构化错误回灌；
> - 会话存储（SQLite / 内存）、技能加载（frontmatter 解析）、结构化日志（zap）。

---

## 一、生产级能力差距分析

下表按"是否阻断生产使用"标注优先级（P0 阻断 / P1 高 / P2 中）。

| 能力 | 当前状态 | 缺口 | 优先级 |
|------|----------|------|--------|
| **循环终止保护** | `runLoop` 无 `maxTurns` 上限，仅依赖模型主动 `stop` | 模型死循环/重复调用会无限烧钱；无"卡住检测" | P0 |
| **工具错误自愈** | 任一工具返回 `IsError` 后直接 `return errToolCallFailed` 中断整个会话（`agent_loop.go:294`） | 单次工具失败即终止，违背"自愈"设计意图；错误已写入上下文却不让模型重试 | P0 |
| **工具输出截断** | 工具结果整段追加进上下文 | 大文件/长命令输出撑爆上下文窗口、放大 token 成本 | P0 |
| **危险命令拦截 / HITL** | `executeTools` 审批回调为 TODO（`agent_loop.go:221`），`Run` 模式 `Ask` 视为 `Allow` | 无破坏性命令（rm -rf / 强制推送等）防护，无人工确认 | P0 |
| **Token 计量 / 上下文窗口** | `emitter.tokenUpdate` 两处均为 `panic("not imp")`（`agent_loop.go:318`、`stream.go:77`） | 完全无 token/窗口感知，长会话必然越窗失败 | P0 |
| **上下文压缩（Compaction）** | `emitter.compaction` 被注释为 TODO，无实现文件 | 长对话无法收敛，质量随时间下降 | P1 |
| **会话续跑（resume）** | `LoadHistoryContext` 标注 TODO，仅返回 system+user；`main.go` 每启动生成新 `sessID`，`--resume-id` 解析后从未使用 | `--resume` / `--continue` 不可用，无法中断后接续 | P1 |
| **Skills 真实注入** | `SkillDispatcher.Execute` 仅返回"已分发"字符串，未把 `Skill.Body` 注入上下文；`UseSkillTool` 缺位 | 技能是空壳，无法真正驱动行为 | P1 |
| **Skill 目录扫描范围** | `LoadSkillFromDir` 直接 `os.ReadDir(homeDir/workDir)` 顶层，**含 `.git`/`cmd`/`internal`/`logs` 等** | 日志已出现大量"加载技能失败"噪声，应限定到专用目录（如 `skills/`） | P1 |
| **无头 / 非交互模式** | `main.go` 只启动 TUI（`tea.NewProgram`） | 无法在 CI / 管道 / 自动化中运行 | P1 |
| **多 Provider 路由** | 仅 `OpenAIProvider`，无 Anthropic/本地模型路由 | 单点依赖、无法按任务选模型 | P2 |
| **Sub-Agent 委派** | 无 | 复杂任务无法并行/委派，上下文易溢出 | P2 |
| **长期记忆（LTM）** | 仅会话级短期记忆，`Config` 有 `LtmConfig` 占位但无实现 | 跨会话知识无法沉淀 | P2 |
| **可观测性（Metrics/Tracing）** | 仅 zap 日志，无指标/链路 | 生产排障、成本核算缺数据 | P2 |
| **Sandbox 隔离** | `bash` 直接在宿主环境执行 | 缺容器级隔离与安全加固 | P2 |

> 结论：**P0 全部集中在"可靠性与安全防护"**，必须在第 1 周解决，否则无法在生产环境放量。

---

## 二、4 周实施计划

### 第 1 周：循环可靠性与安全防护（P0 清零）

**目标**：让 Agent 在任何异常下都"可控、可恢复、不越界"。

- **D1 循环终止保护**
  - `runLoop` 增加 `maxTurns`（默认 30，可由 `WithMaxTurns` 配置）；超限后以"已达最大步数"作为最终文本回复收尾，而非 panic。
  - 增加"重复调用检测"：若连续 N 轮 `tool_calls` 的 (name+args) 与前一轮完全相同，判定卡住并终止+告警。
  - 涉及：`internal/engine/agent_loop.go`

- **D2 工具错误自愈（关键修复）**
  - 删除 `agent_loop.go:294` 的 `if hasErr { return errToolCallFailed }`；改为：错误结果已写入上下文（`IsError=true`），直接 `continue` 进入下一轮，让模型自我修正。
  - 仅当**工具超时/panic 级不可恢复**时才中断；区分"工具业务错误"（可自愈）与"执行基础设施错误"。
  - 涉及：`internal/engine/agent_loop.go`

- **D3 工具输出截断**
  - 在 `registry.Execute` 或 `executeTools` 处对 `res.Output` 做上限截断（默认 20KB / 可配），超限尾部追加 `...[已截断，共 N 字节]` 并建议改用 `read` 分段查看。
  - 涉及：`internal/tools/registry.go`、`internal/engine/agent_loop.go`

- **D4 危险命令拦截（DangerHook 雏形）**
  - 实现 `internal/hooks` 包：`MatchDangerous(cmd string) (bool, reason)` 覆盖 rm -rf、git reset --hard、force-push、DROP TABLE、:(){ 等高危模式。
  - `executeTools` 在真正执行 `bash` 前插入检查；命中后：流式模式发 `EventApprovalRequired`，非流式模式默认 deny 并记录。
  - 涉及：新增 `internal/hooks/`，`internal/engine/agent_loop.go`

- **D5 修正 Skill 目录扫描**
  - `SkillLoader` 增加 `skillDirs []string` 配置项（默认 `skills/`、`~/.config/<app>/skills`），不再扫描 homeDir/workDir 顶层；`LoadSkillFromDir` 仅扫描该专用目录。
  - 涉及：`internal/skill/skill_loader.go`

**周验收**：
- 构造"模型反复调用同一工具""工具故意失败""bash 执行 rm -rf /"三类用例，Agent 均不崩溃、可恢复/可拦截；
- 大输出被截断；
- 启动日志不再出现 `加载技能失败` 噪声。

---

### 第 2 周：Token 感知、上下文压缩与会话续跑（P0 收尾 + P1）

**目标**：让 Agent 具备"上下文工程"能力——知道自己在窗口的什么位置，并能收敛。

- **D6 替换 token 计量 panic**
  - 实现 `TokenEstimator`（字符数 ÷ 4 近似，或接入 tiktoken 类库做精确估算）。
  - `emitter.tokenUpdate` 改为真实上报 `tokens / window`；`runLoop` 维护当前上下文 token 量，接近窗口 80% 触发压缩。
  - 涉及：`internal/engine/agent_loop.go`、`internal/engine/stream.go`、新增 `internal/token/`

- **D7 上下文压缩（Compaction）**
  - 实现 `SummarizationCompactor`：达到阈值时用一次轻量 LLM 调用对早期（非最近 K 轮）消息做摘要替换；保留最近窗口与所有 tool 结果对。
  - 维护"孤立工具对"修复：被摘要的历史若含 tool_call，需连同其 tool_result 一起处理，避免破坏对话结构。
  - 失败回退：`TokenBudgetCompactor`（按预算截断）兜底。
  - 涉及：新增 `internal/compaction/`，`internal/engine/agent_loop.go`

- **D8~D9 会话续跑（resume）**
  - 真正实现 `LoadHistoryContext`：从 `memory.Session.GetMessages` 读取历史，拼回 system+history+user；修复"system prompt 被重复写入历史"的隐患（存储历史时**不含** system 消息，仅对话轮次）。
  - `cmd/main.go` 接入 `--resume-id`：传入则复用该 `sessID`，`SaveHistoryContext` 以 `startIndex` 增量落库；新增 `--continue`（续上一次会话）。
  - 涉及：`internal/engine/agent_loop.go`、`cmd/main.go`、`internal/memory/`

**周验收**：
- 长对话（>窗口 80%）自动压缩且关键信息不丢；
- 中断后 `--resume` 无损续跑；
- tokenizer 估算与真实用量偏差 < 20%。

---

### 第 3 周：技能真实化、执行增强与无头模式（P1）

**目标**：让技能"能用"、让 Agent 能嵌入自动化流水线。

- **D10~D11 Skills 真正生效**
  - `SkillDispatcher.Execute` 改为：把命中 `Skill.Body`（含渐进式披露的 references/scripts 索引）作为一条 `system`/`user` 消息注入后续上下文，并返回"已加载技能 X 的内容到上下文"；
  - 新增 `UseSkillTool` 语义：模型可显式 `skill_name` 触发按需加载，避免启动即注入全部技能上下文。
  - 涉及：`internal/skill/skill_dispatcher.go`、相关 prompt 组装。

- **D12 工具结果流式与策略**
  - `executeTools` 支持"按结果顺序回填"并发结果，避免乱序；为长时间命令（如构建）预留进度事件（复用 `EventToolStart`/`EventToolResult`）。
  - 涉及：`internal/engine/agent_loop.go`、`internal/engine/stream.go`

- **D13 无头 / 非交互模式**
  - `cmd/main.go` 新增 `--print`（或检测非 TTY）：跳过 TUI，直接 `agent.Run` 并打印最终文本 + 工具调用摘要；支持从 stdin 读 prompt。
  - 支持 `--output json` 输出结构化事件（便于 CI 解析）。
  - 涉及：`cmd/main.go`、`internal/engine/`

- **D14 多 Provider 路由预留**
  - `provider` 包抽象 `NewProvider(cfg)` 工厂，按 `model` 前缀（openai-/anthropic-/ollama-）选择实现；本周至少把 OpenAI 走通，Anthropic 列为 P2 延伸。
  - 涉及：`internal/provider/`、`internal/config/`

**周验收**：
- 调用技能后上下文出现该技能正文，模型据此行动；
- `echo "..." | app --print` 在无 TTY 环境正常产出；
- `--output json` 可被脚本解析。

---

### 第 4 周：可观测、信任态与工程化收尾（P2）

**目标**：生产可运维、可排障、可发布。

- **D15~D16 可观测性**
  - 实现 `tokenUpdate` 的真实指标：累计 token、每轮耗时、工具成功率；提供 `noop` / `stdout` Exporter。
  - 关键路径埋点：LLM 调用 Span、工具执行 Span（复用 `EventToolStart/ToolResult` 的耗时）。
  - 涉及：新增 `internal/observability/`，`internal/engine/`

- **D17 REPL 信任态**
  - Trusted / Untrusted / Undecided 三态持久化；首次运行危险操作弹确认，`--approve` 会话级授权；D4 的 DangerHook 与信任态联动（Untrusted 默认 ask）。
  - 涉及：`cmd/main.go`、`internal/hooks/`

- **D18 配置与模型选择增强**
  - `Config` 增加 `Models`（多模型）、`Compaction`、`Token` 阈值、`Sandbox` 开关；把占位 `LtmConfig`/`MemoryConfig` 落地为可选项（LTM 至少打通 store+search 最小闭环）。
  - 涉及：`internal/config/`

- **D19~D20 回归、文档与发布就绪**
  - 补全 `agent_loop` 关键路径单测（maxTurns/自愈/截断/压缩/续跑）；
  - 编写 `README` 的"生产部署"小节、本计划的进度回填；
  - 提供最小 `Dockerfile` + `.env.example`。
  - 涉及：仓库根。

**周验收**：
- 一次长跑可输出 token/耗时/工具成功率指标；
- 信任态在重启后保持；
- `docker build` 可出镜像，README 含生产部署说明。

---

## 三、每周验收清单（自检）

| 周 | 必须能跑通 | 必须存在的测试 |
|----|------------|----------------|
| W1 | 工具失败自愈、危险命令被拦截、大输出截断、启动无技能噪声 | `runLoop` 终止/自愈/截断 + `hooks.MatchDangerous` |
| W2 | 长会话自动压缩、中断后 `--resume` 续跑、token 计量生效 | compaction（摘要不丢关键信息）+ session 序列化 + token 估算 |
| W3 | 技能被真实注入、`echo ... \| app --print` 非 TTY 产出 | skill 注入 + 无头模式冒烟 |
| W4 | 指标可输出、信任态持久、镜像可构建 | observability 导出 + 信任态持久化 |

---

## 四、风险与依赖

1. **压缩质量依赖 LLM 摘要能力**：D7 必须配"失败回退"，避免压缩本身出错拖垮主流程（fail-open）。
2. **token 估算偏差**：字符÷4 仅为近似，生产前建议接入精确 tokenizer；窗口阈值按模型实际 `context_window` 配置。
3. **Skills 注入会放大上下文**：D10 的"按需加载"比"全量注入"更安全，优先实现 `UseSkillTool`。
4. **审批/HITL 涉及交互范式**：流式 TUI 审批（D4）与无头模式（D13）需两套路径，注意 `Ask` 在非 TTY 下默认 deny。
5. **优先级纪律**：W1 的 P0 是放量前置条件，任何功能不得阻塞 P0 完成。
