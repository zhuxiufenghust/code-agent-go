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
  - 在 `registry.Execute` 或 `executeTools` 处对 `res.Output` 做**统一上限截断**（默认 20KB / 可配），超限仅保留前缀并在尾部追加 `...[已截断，共 N 字节]`。
  - 截断对**所有工具**生效，但**提示文案必须按工具类型区分**，避免对 `bash` 输出提示「用 read 分段查看」这类无效建议：
    - `read_tool`：提示用 `start_line`/`end_line` 或 `offset`/`limit` 重新读取所需片段；
    - `bash_tool`：提示用 `head`/`tail`/`grep`/`sed` 过滤，或将输出重定向到文件后用 `read` 分段查看；
    - `web_fetch_tool` / `web_search_tool`：提示缩小范围或改用更精确的 query；
    - 其它工具：给出「缩小请求范围 / 分多次获取」通用兜底。
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

## 二·五、进度回填（截至 W4 收尾）

> 图例：✅ 已落地（含测试）｜🟡 部分落地｜❌ 未开工
>
> 说明：行号引用已失效时以"落点文件"为准。

### 第 1 周：可靠性与安全防护（P0）

| 项 | 状态 | 落点 |
|---|---|---|
| D1 循环终止保护（maxTurns + 卡住检测） | ✅ | `internal/engine/agent_loop.go`（`WithMaxLoopTurns`、`toolCallsSignature`）；测试 `TestStreamRun_MaxLoopTurns`、`TestStreamRun_StuckDetection` |
| D2 工具错误自愈 | ✅ | `executeTools` 后错误回灌、`context_mng.RecoveryManager`；测试 `TestRun_ToolError` |
| D3 工具输出截断（按工具类型给建议） | ✅ | `internal/tools/registry.go`（`truncateToolOutput`/`truncationHint`）；测试 `registry_test.go` |
| D4 危险命令拦截 + 审批闭环 | ✅ | `internal/hooks/danger.go`（9 条规则）、`internal/tools/approve.go`；测试 `danger_test.go`、`approve_test.go` |
| D5 Skill 目录扫描范围 | ✅ | `internal/skill/skill_loader.go` 只扫 `.config/code-agent-go/skills`（用户级 + 工作区级），不再扫 home/workDir 顶层 |

### 第 2 周：Token 感知、压缩与续跑

| 项 | 状态 | 落点 |
|---|---|---|
| D6 token 计量取代 panic | ✅ | `report.Emitter.TokenUpdate`、`AgentEngine.usage`、`EventUsage`；测试 `usage_test.go` |
| D7 上下文压缩（摘要 + 令牌兜底 + 孤立工具对修复） | ✅ | `internal/context_mng/`（sliding/token/summarization）；测试 `compaction_test.go`、`summarization_compactor_test.go` |
| D8~D9 会话续跑 | ✅ | **续跑本体可用**：同一 `-s <id>` 二次运行实测 `history_msg_count=5 / start_index=5`（历史被还原且增量落库），`SaveHistoryContext` + `persistence_test.go` 完整；仍缺 `--resume/--continue` 便捷入口，`LoadHistoryContext` 里的 TODO 注释已过时（实现就在下面），缺"历史还原"专项用例 |

### 第 3 周：技能、流式与无头模式

| 项 | 状态 | 落点 |
|---|---|---|
| D10~D11 Skills 真正注入 + 按需加载 | ✅ | `internal/skill/skill_dispatcher.go` 按 `skill_name` 注入 Skill.Body（幂等，重复加载只回执）；测试 `skill_dispatcher_test.go` |
| D12 工具结果按序回填 + 耗时事件 | ✅ | `executeTools` 按下标回填、`EventToolResult` 携带 `Duration` |
| D13 无头 / 非交互模式 | ✅ | 新增 `internal/headless/`（`Runner` 消费 `StreamRun` 事件、`ShouldRunHeadless` TTY 判定、`ReadPrompt`）；`cmd/main.go` 接入 `-p/--print`、`--output text\|json`、`--show-thinking`，详见下方"W4 复核补记"与新增小节 |
| D14 多 Provider 路由 | ✅ | W4 补上了 `provider.Register/New` 工厂与 `models` 段（`internal/provider/factory.go`），但仅 OpenAI 兼容实现一种，Anthropic 仍未落地 |

### 第 4 周：可观测、信任态与工程化收尾

| 项 | 状态 | 落点 |
|---|---|---|
| D15 token/耗时/工具成功率指标 | ✅ | 新增 `internal/observability/`（`Metrics` 接口、`Snapshot` 聚合）；`internal/observability/observability_test.go` |
| D15 noop / stdout Exporter | ✅ | `observability.Nop()` / `observability.NewStdout(w, interval)`，输出单行 JSON |
| D16 LLM / 工具 Span 埋点 | ✅ | `internal/observability/span.go` + `Instrument()` 织入 `report.Emitter`；`instrument_test.go` |
| D17 信任态三态 + 持久化 | ✅ | 新增 `internal/trust/`（`~/.code-agent-go/trust.json`）；`internal/tools/approve.go` 接入 `trust.Decide` |
| D17 `--approve` 会话级授权 | ✅ | `cmd/main.go`（`-y/--approve` 临时 trusted；`--trust <level>` 持久化） |
| D18 `Models` 多模型 / `Compaction` / `Token` 阈值 | ✅ | `config.Models/Compactor/Token`（`token.contextWindow` 为单一来源）+ `provider.New` 路由 |
| D18 `Sandbox` 开关 | ✅ | `config.Sandbox` + `internal/sandbox/`（Local / Docker 执行体，缺 docker 直接报错） |
| D18 `LtmConfig` / `MemoryConfig` 落地 | ✅ | `internal/ltm/`（SQLite store/search）+ `remember_tool`/`recall_tool`；`memory.persistence` 开关 |
| D19 agent_loop 关键路径单测 | ✅ | maxTurns/卡住/自愈/截断/压缩均有用例；续跑仍缺 `LoadHistoryContext` 还原用例 |
| D20 README 生产部署小节 | ✅ | `README`："生产部署"（镜像构建/运行/采集/安全模型）+ 已知限制 |
| D20 Dockerfile / `.env.example` | ✅ | 仓库根 `Dockerfile`（distroless 静态二进制）、`.dockerignore`、`.env.example`（区分运行期与测试变量） |

### W4 周验收自检

| 验收项 | 结论 |
|---|---|
| 一次长跑可输出 token/耗时/工具成功率指标 | ✅ `observability.enabled` 或 `--metrics` 打开即可（详见 README"可观测性"） |
| 信任态在重启后保持 | ✅ `--trust trusted` 落盘后跨进程生效；`--approve` 仅限本次进程 |
| `docker build` 可出镜像，README 含生产部署说明 | ✅ `make docker-build`；容器内交互用 `-it`，流水线直接 `-p` 即可（不再需要 TTY） |

### W4 复核补记（本机实测，非静态审查）

> 方式：`gofmt -l` / `go vet ./...` 零告警；`go test -count=1 ./...` 14 个包全绿；
> 二进制以隔离 `HOME` 实跑；临时用例验证后已删除，未入库。

| 复核项 | 实测结论 |
|---|---|
| D15/D16 导出内容 | ✅ 一次含"工具成功 + 工具失败"的 Run 拿到 `{"type":"metrics",...}`：`elapsed_ms`、`llm.calls=2`、`tool.success_rate=0.5` 且 `by_tool` 有分工具明细、`token.total_tokens=280`；Span 同 `trace_id` 串成 `agent.run → llm.generate / tool.<name>`，失败工具的 span 带 `error` 字段 |
| D17 跨进程持久 | ✅ `--trust trusted` 写盘 → 无参数再启动仍读到 `trusted`；`-y` 只管本次进程（`session_only:true`）且不改写 `trust.json`；`--trust undecided` 可回落 |
| D18 Sandbox 实跑 | ✅ Docker 执行体真跑通（`echo` 输出回传、`-w /workspace` 挂载正确）；`readOnlyRootfs=true` 实测拦住工作目录写入。⚠️ 该开关只把**工作目录**挂 `:ro`，容器自身 rootfs 仍可写，字段名易被误解成"整个根只读"（README 表述是准确的） |
| D18 LTM 落地 | ✅ `ltm.enabled=true` 启动时注册 `remember_tool`/`recall_tool` 并在 `~/.code-agent-go/data` 建库 |
| D20 镜像可运行 | ✅ `make docker-build` 出 `code-agent-go:latest`（19.3MB）；`docker run --rm` 静态二进制正常启动并加载配置（distroless + nonroot） |

**复核当时暴露的遗留项及处置**

1. ~~**D13 无头模式缺失构成硬失败**：非 TTY 环境下进程直接 `could not open a new TTY` 退出~~
   → 已修复，见下方"W3 遗留补齐：无头模式（D13）"。
2. ~~**该退出路径丢最后一帧指标**：提前 `os.Exit` 使 `defer obs.Close()` 不执行~~
   → 已修复：`main` 改为 `os.Exit(run())`，退出码冒泡，`defer` 全部按序执行。
3. **D8 `--resume` / `--continue` 便捷入口缺位**：`LoadHistoryContext` 里的 TODO 注释已过时
   （下面就是实现），续跑本体经 `-s <session-id>` 实测可用；差的只是"一键续上次"的入口
   与一条历史还原用例。
4. **差距分析表里的"Sub-Agent 委派"（P2）在 4 周计划里没有任何对应 D 条目**：属未开工且未排期
   （原先 `internal/engine/stream.go` 里那条指向不存在类型的 `EventSubAgent` 注释已在本次复核中修正）。

### W3 遗留补齐：无头模式（D13）

W4 收尾时补上了这条原来标红的项，并入 `week_seven`：

| 能力 | 落点 |
|---|---|
| 非交互运行入口 | `internal/headless/runner.go`：`Runner.Run` 消费同源于 TUI 的 `StreamRun` 事件流 |
| TTY 判定 | `ShouldRunHeadless`：`-p` 无条件无头；stdout 非终端（重定向/管道）或 stdin 为管道即无头 |
| 任务输入 | `ReadPrompt`：位置参数 > stdin（stdin 为终端时不读，避免等 EOF 假死） |
| 输出形态 | `text`（stdout 只放助手正文、诊断走 stderr）/ `json`（全部单行 JSON，末行 `type=result`） |
| 审批降级 | 高危命令在无 TTY 下自动拒绝并回灌模型自愈；要放行必须显式 `-y`/`--trust trusted` |
| 退出码 | `0` 完成 / `1` 参数或运行错误 / `130` 被 SIGINT、SIGTERM 中断 |

**实跑验证**（本地假 OpenAI 兼容服务 + 真二进制）：

- `-p "你好" > answer.md`：stdout 只有答案，工具调用与 `—— 轮次=2 工具调用=1 ...` 摘要在 stderr；
- `echo "任务" | code-agent-go`：无 `-p` 也自动走无头并产出答案；
- `--output json`：7 行事件中末行为 `{"type":"result",...}`，可被 `jq` 直接筛选；
- 高危 `rm -rf`：`高危命令被拒=1`、目标目录**仍在**；加 `-y` 后同一条命令真的执行（控制变量对照）；
- 运行中 SIGINT：退出码 `130`，并且退出前仍然导出最后一帧 `{"type":"metrics"}`；
- 缺输入：打印使用提示并退出 `1`。

上述场景已固化为 `make smoke-headless`（`scripts/fake_openai.py` + `scripts/smoke_headless.sh`），
7 组断言全绿，不需要 Key / 网络，可直接进 CI。

### 全表复核结论（最新一轮）

逐项对照代码与实测，除以下三条外均已落地：

| 缺口 | 现状 |
|---|---|
以上三条缺口均已补齐（并入 `week_seven`，逐项带单测 + 实跑验证）：

| 缺口 | 现在的状态 |
|---|---|
| `--resume` / `--continue` 入口 | ✅ `--resume ID` 显式复用；`--continue` 续"上一次会话"（`last_session` 原子落盘）；与 `-s` 冲突时报冲突 |
| Anthropic Provider | ✅ `internal/provider/anthropic.go`：Messages API + SSE，`system` 独立字段、`max_tokens` 必填、`tool_use`/`tool_result` 内容块；注册进工厂 |
| Sub-Agent 委派 | ✅ `internal/subagent`：`delegate_tool` 并行委派，知识隔离 + 轮次/并发/嵌套/耗时/回灌长度预算；`subagent.enabled` 开启 |

使用手册（按能力组织的用法与验证方式）见 `docs/features-and-usage.md`。

**未被覆盖的一条验收口径**：W2 周验收要求"tokenizer 估算与真实用量偏差 < 20%"。
当前 `context_mng.EstimateTokens` 仍是 `字符数/4 + 每条消息固定开销`
（`compact.go:15/44`）的近似，没有量化过偏差，属已知近似而非实测达标；
需要精确控制成本时应接入真实 tokenizer（见"四、风险与依赖"第 2 条）。

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
