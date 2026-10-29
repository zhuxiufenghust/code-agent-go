# code-agent-go 已实现功能与使用手册

> 面向"用起来"的一页纸：每项能力对应**怎么用**、**落在哪**、**怎么验证**。
> 计划与差距分析的进度回填见 `docs/production-roadmap-4weeks.md`。

## 0. 两种运行形态

| 形态 | 触发条件 | 用途 |
|---|---|---|
| 交互 TUI | stdin 与 stdout 都是终端 | 边聊边改 |
| 无头 | `-p/--print`，或 stdout 被重定向，或 stdin 是管道 | CI / 管道 / 容器定时任务 |

```bash
make build
./bin/code-agent-go -c conf/config.json                # 交互
./bin/code-agent-go -c conf/config.json -p "读 README 总结三段" > answer.md
cat task.txt | ./bin/code-agent-go -c conf/config.json  # 管道输入，自动无头
./bin/code-agent-go -c conf/config.json -p "hi" --output json | jq 'select(.type=="result")'
```

无头模式约定：

- `text` 模式：stdout 只有助手正文，工具调用/摘要/日志全部走 stderr；
- `json` 模式：stdout 每行一个结构化事件，末行 `{"type":"result",...}` 带轮次、工具统计、token 用量；
- 高危命令无审批通道时**默认拒绝**并回灌模型自愈，要放行必须显式 `-y` 或 `--trust trusted`；
- 退出码：`0` 完成 / `1` 参数或运行错误 / `130` 被 SIGINT、SIGTERM 中断。

## 1. 会话与续跑

```bash
./bin/code-agent-go -s my-session -p "继续昨天那个重构"        # 指定（或新建）会话
./bin/code-agent-go --resume my-session -p "继续"              # 明确复用某个会话的历史
./bin/code-agent-go --continue -p "接着上次那件事"             # 续"上一次会话"
```

- 会话与最近会话记录都在 `~/.code-agent-go/`（会话库 `data/sessions.db`、最近会话 `last_session`）；
- `--continue` 读取 `last_session`（每次启动都会原子更新），首次运行没有"上一次"会明确报错；
- `--continue` 与 `-s/--session-id`、`--resume` 同时给出会直接报冲突（避免接错上下文）；
- `memory.persistence: false` 可让本次运行完全不落库。

## 2. 模型路由（多 Provider）

配置 `models.items`，用 `--model/-m` 选择；`provider` 缺省时按模型名前缀推断（`claude-`/`anthropic-` → anthropic，`ollama-` → ollama，其余 → openai 兼容）。

```jsonc
{
  "models": {
    "default": "fast",
    "items": [
      { "name": "fast", "provider": "openai",   "model": "gpt-4o-mini",
        "baseURLEnv": "OPENAI_BASE_URL", "apiKeyEnv": "OPENAI_API_KEY" },
      { "name": "claude", "provider": "anthropic", "model": "claude-3-5-sonnet",
        "baseURLEnv": "ANTHROPIC_BASE_URL", "apiKeyEnv": "ANTHROPIC_API_KEY", "maxTokens": 8192 }
    ]
  }
}
```

- 已注册实现：`openai`、`openai-compatible`、`ollama`、`anthropic`；
- Anthropic 走 Messages API：`system` 独立字段、`max_tokens` 必填、`tool_use`/`tool_result` 内容块、
  流式按 `content_block` 累加（工具入参 JSON 分片）；
- 未注册的 provider 会直接报错并列出已注册项，不会静默回落成 OpenAI 兼容。

## 3. Sub-Agent 委派

```jsonc
{
  "subagent": {
    "enabled": true,
    "maxDepth": 1,
    "maxConcurrent": 3,
    "maxTurns": 8,
    "maxOutputChars": 8000,
    "timeoutSecs": 180,
    "excludeTools": ["write_tool", "edit_tool"]
  }
}
```

启用后模型会多出一个 `delegate_tool`：主 Agent 把彼此独立的子任务并行派给子 Agent，
每个子 Agent 跑在**自己的上下文**里（看不到主会话历史），只回传结构化结论
（含 `summary`、轮次、工具成功/失败数、被拒审批数、耗时、状态 `ok/truncated/timeout/max_turns/error`）。

隔离与预算：

- **知识隔离**：子会话独立、不落库，主上下文不会被排查过程污染；
- **最大轮次**：`maxTurns`（子任务可覆盖，但不得超过该上限）；
- **工具预算**：工具结果沿用注册表 20KB 截断，结论再按 `maxOutputChars` 截断；
- **并发**：`maxConcurrent` 同时限制"一次能派几个"和"同时跑几个"；
- **嵌套深度**：默认 1（子 Agent 不能再委派），委派工具在子 Agent 的工具视图里被隐藏，硬调也会被拒；
- **危险命令**：子 Agent 走同一套高危规则，无审批通道即拒绝，不会阻塞主 Agent 询问审批人员。

## 4. 安全：高危命令拦截与信任态

| 机制 | 落点 | 行为 |
|---|---|---|
| 内置高危规则 | `internal/hooks` | rm -rf、force push、`DROP TABLE`、fork 炸弹等 9 条 |
| 配置黑名单 | `tool.dangerCmds` | 正则匹配，按词边界锚定 |
| 人工审批 | `internal/tools/approve.go` | 有交互通道时弹窗确认，无通道时默认拒绝 |
| 信任态 | `internal/trust` | `undecided`（默认，逐个问）/ `trusted`（放行）/ `untrusted`（每次都问，无通道则拒） |

```bash
./bin/code-agent-go --trust trusted      # 落盘，跨进程生效（~/.code-agent-go/trust.json）
./bin/code-agent-go -y -p "删掉构建缓存"  # 仅本次进程放行
```

## 5. 沙箱

`sandbox.enabled: true` 时 bash 跑进一次性容器（`--network none`、不继承宿主 env、工作目录可只读挂载），
缺 docker 直接报错而不是悄悄回落宿主机。

## 6. 上下文工程

- `token.contextWindow` 是唯一来源（压缩阈值与水位预警都取它，`compactor.contextWindow` 仅兼容）；
- 超过 80% 打 `token_window_high_water` 日志，超预算时按 `compactor.strategy` 压缩：`summarization`（LLM 摘要，
  失败回退 `token` 预算截断）/ `token` / `sliding`；
- 工具输出统一截断并**按工具类型给建议**（read 用行号分段、bash 用 `head/grep`、web 缩小范围等）。

## 7. 可观测性

`observability.enabled`（或 `--metrics`）打开后按 `dumpIntervalSecs` 输出单行 JSON：
`{"type":"metrics","snapshot":{...}}`（累计 token、LLM/工具耗时与成功率、压缩次数）与
`{"type":"span","span":{...}}`（`agent.run → llm.generate / tool.<name>` 同 trace_id 串联）。

## 8. 长期记忆与技能

- `ltm.enabled: true` 注册 `remember_tool` / `recall_tool`，跨会话沉淀事实与偏好（SQLite + LIKE 检索）；
- 技能目录固定为 `~/.config/code-agent-go/skills` 与工作区 `.config/code-agent-go/skills`，
  由 `skill_dispatcher` 按 `skill_name` **按需**把正文注入上下文（幂等，重复加载只回执）。

## 9. 生产部署

```bash
make docker-build
docker run -it --rm -e OPENAI_API_KEY=... -e OPENAI_BASE_URL=... \
  -v "$HOME/.code-agent-go:/home/nonroot/.code-agent-go" -v "$(pwd):/workspace" -w /workspace \
  code-agent-go:latest -c /app/conf/config.json                      # 交互
docker run --rm -e OPENAI_API_KEY=... code-agent-go:latest \
  -c /app/conf/config.json -p "跑一遍 go test 并汇报失败用例"          # 无头，无需 TTY
```

## 10. 自检与冒烟

```bash
make check            # gofmt + go vet + 全量单测
make test-live        # 连真实 LLM 的集成测试（需要 .env）
make smoke-headless   # 无头模式端到端冒烟（不需要 Key/网络，7 组断言）
```
