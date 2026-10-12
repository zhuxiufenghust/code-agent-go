# review skill 自测方案

用于验证 skill **能被发现 → 能触发 → 能按流程执行 → 能覆盖缺陷 → 不误报**。
任何一次修改 `SKILL.md` / `references/checklist.md` / `REFERENCE.md` 后都应重跑本流程，
并把结果（召回率、误报数、漏报分类）记到结果文件里，形成趋势。

---

## L0：结构自检（无需启动 opencode，秒级）

在项目根目录执行：

```bash
cd .opencode/skills/review

# 1) frontmatter 三个字段齐备，且 name 与目录名一致
sed -n '1,6p' SKILL.md

# 2) 本地链接全部真实存在（引用不存在的 md = 后面一定"查不到"）
for f in references/checklist.md REFERENCE.md SELFTEST.md fixtures/EXPECTED.md; do
  [ -f "$f" ] && echo "OK  $f" || echo "MISSING  $f"
done

# 3) SKILL.md 里引用的本地文件是否都能打开
rg -o '\]\(([^)]+\.md)\)' SKILL.md | sort -u
```

## L1：fixtures 可用性（确定性前置）

```bash
cd .opencode/skills/review/fixtures

go build ./...                    # 必须 BUILD OK
gofmt -l .                        # 期望有输出（fixtures 故意不追求格式），但不报错
go vet ./...                      # 期望至少报 concurrency_leak.go 的 copylocks
```

判定标准：

| 检查 | 期望 | 说明 |
|---|---|---|
| `go build` | 退出 0 | fixture 本身要能编译，否则 AI 会在"这是语法问题"上浪费输出 |
| `go vet` | 至少 1 条 copylocks | 证明 Step 3a 的确定性工具有东西可抓 |

> 主仓库应不受影响：fixtures 是嵌套 go.mod，`go build ./...` 在仓库根不会编它。
> 验证：`cd <repo> && go build ./...`

## L2：发现性（skill 能不能被加载）

1. **改完配置必须重启 opencode**（已加载的配置不会热更新）
2. 重启后在 code-agent-go 仓库内确认 projectskill 生效：

```bash
# 项目级 skill 目录（应存在）
ls .opencode/skills/review/SKILL.md
# 全局同名 skill（仍存在，但在本仓库内被项目级覆盖）
ls ~/.opencode/skills/review/SKILL.md
```

> **重要**：本 skill 名字沿用 `review`，在 code-agent-go 仓库内会**覆盖**全局同名 skill；
> 在其他目录下仍是全局版本。若想两者并存，把 `.opencode/skills/review` 改名成
> `go-review` 并把 frontmatter 的 `name:` 同步改成 `go-review`（二者必须与目录名一致）。

## L3：触发（该响的时候响，不该响的时候别响）

在 code-agent-go 仓库内依次用下面的话术，检查是否自动加载了本 skill：

**应触发（positive）**

- `review 一下 .opencode/skills/review/fixtures/concurrency_leak.go`
- `帮我 CR 一下 fixtures/security.go`
- `这段代码有问题吗`（上下文是某个 .go 文件）
- `check my changes`（fixtures 有未提交改动时）

**不应触发（negative）**

- `git status`（纯 git 操作）
- `这个项目怎么编译`（非审查意图）
- `帮我 review 一下这份文档`（`docs/*.md`，非 Go 代码 —— 允许不触发）

## L4：流程合规性（报告骨架）

对 `fixtures/` 跑一次完整审查，报告必须包含：

| 必备项 | 检查点 |
|---|---|
| 🔧 工具扫描结果 | 出现 `gofmt`/`go vet` 原始输出或明确的 `[tool-unavailable: xxx]` |
| 分级 | 至少 `🔴 Critical` 与 `🟡 Important` 两段 |
| 定位 | 每条都带 `path/file.go:line` |
| 修复建议 | 每条都给了修改后的代码片段 |
| ✅ 优秀实践 | 非空（哪怕只肯定一处） |
| 📊 审查总结 | 有统计数字 |

缺项说明 skill 的执行流程被跳过或未生效，需要修正 SKILL.md 的指令。

## L5：召回率（核心）

```bash
# 逐个文件跑，避免一次塞太多导致跳过
/review .opencode/skills/review/fixtures/concurrency_leak.go
/review .opencode/skills/review/fixtures/security.go
/review .opencode/skills/review/fixtures/errors_panic.go
```

对照 [fixtures/EXPECTED.md](fixtures/EXPECTED.md) 的 30 个条目打分：

```
召回率 = 命中条目数 ÷ 30，目标 ≥ 80%
```

判定：

- **< 60%**：流程失效，检视 Step 3b 的全局/细节两段是否被真的读和执行
- **60% ~ 80%**：可用但需在 checklist 中补充对应模式的描述
- **≥ 80% 且无误报**：基线达成

重点看**整类漏报**：即使总分达标，若"并发泄漏 4 项全没报"或"命令注入/SSRF 全没报"，仍应视为不通过，
因为这往往意味着对应章节写得太抽象、AI 不会落地到具体代码模式。

## L6：稳定性与误报

- **可重复性**：同一份 fixture 连跑 2 次，问题条目集合应基本一致（允许表述不同，但不得在 30 项上跳变）
- **误报率**：对照 EXPECTED.md 的"反向对照（确保不误报）"清单；任何把合规写法判为问题的都计入
- **空白对照**：挑一段你已确认没有问题的仓库内 Go 代码跑一次，不应产出 Critical；
  对拿不准的点应标 `[question]` 请作者确认，而不是假定是 bug

## L7：回归记录

每次自测后把结果追加到一个记录文件（例如 `.opencode/skills/review/SELFTEST-RESULT.md`）：

```markdown
## YYYY-MM-DD

- 版本号/commit：<commit>
- 工具：go vet=有 / golangci-lint=无 / staticcheck=无
- 召回率：24/30 (80%)
- 误报：2（分别是 错误吞没里的 X、并发里的 Y）
- 整类漏报：无
- 结论：通过 / 不通过（原因）
```

三次不通过必须回到 checklist 修订规则描述，而不是反复重跑期待偶然命中。
