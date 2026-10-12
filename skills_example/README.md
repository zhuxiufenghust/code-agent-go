# Skills 示例

本目录是 code-agent-go 技能系统的示例集。目录本身**不会被 agent 加载**，仅作为模板参考。

## 技能格式

每个技能是一个独立目录，目录名必须与 `SKILL.md` frontmatter 中的 `name` 一致：

```
<skill-name>/
├── SKILL.md          # 必需：frontmatter + 正文
├── references/       # 可选：详细参考资料（正文放不下的清单/规范）
├── scripts/          # 可选：可执行脚本
└── assets/           # 可选：其他静态素材
```

`SKILL.md` 结构：

```markdown
---
name: my-skill
description: "一句话说明 + 触发词。description 是模型判断何时加载该技能的唯一依据，触发词要覆盖用户的各种说法。---

（正文：写给模型看的操作指令。技能正文通过 skill_dispatcher 工具按需注入上下文，超过 64KB 会被截断，写不下的内容放 references/ 供模型按需读取。）
```

## 加载机制

agent 启动时从两个位置扫描技能，按名称去重（workDir 级覆盖用户级）：

| 级别 | 路径 | 作用域 |
|------|------|--------|
| 用户级 | `~/.config/code-agent-go/skills/` | 当前用户所有项目 |
| 项目级 | `<工作目录>/.config/code-agent-go/skills/` | 仅该项目 |

## 使用示例

把想要的技能复制到上述任一目录即可启用，例如：

```bash
# 启用单个技能（用户级）
cp -R skills_example/commit ~/.config/code-agent-go/skills/

# 启用全部示例（项目级）
mkdir -p .config/code-agent-go/skills
cp -R skills_example/* .config/code-agent-go/skills/
```

## 示例清单

| 技能 | 说明 |
|------|------|
| `commit` | 分析 diff 并按 Conventional Commits 规范生成提交信息 |
| `test` | Go 表驱动单元测试编写（标准库，覆盖正常/错误/边界路径） |
| `debug` | 系统性调试：复现 → 假设 → 根因 → 回归验证 |
| `refactor` | 行为不变的小步重构，带测试安全网 |
| `review` | Go 代码审查，输出 Critical/Important/Style 分级报告（含 references/ 与自测 fixtures） |

`review` 展示了完整形态：正文只写流程主干，详细规则放 `references/checklist.md` 按需取用，`fixtures/` 提供埋好缺陷的样本用于技能自测。
