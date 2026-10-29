package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	// GetDefinition 内部调用 log.Info，测试环境需先初始化 zap logger，
	// 否则会因 logger 为 nil 而 panic。
	cfg := zap.NewDevelopmentConfig()
	if err := log.NewLogger(&cfg); err != nil {
		fmt.Fprintln(os.Stderr, "初始化日志失败:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// writeSkillDir 在 base 下创建一个名为 name 的技能目录，并写入合法的 SKILL.md。
func writeSkillDir(t *testing.T, base, name, desc string) {
	t.Helper()
	dir := filepath.Join(base, ".config/code-agent-go/skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md 失败: %v", err)
	}
}

func TestSkillDispatcher_GetDefinition_NoSkills(t *testing.T) {
	homeDir := t.TempDir()
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	def := dispatcher.GetDefinition()
	if def.Name != "skill_dispatcher" {
		t.Errorf("期望 tool name 为 skill_dispatcher, 实际 %q", def.Name)
	}
	if def.Description != "没有可用的技能。" {
		t.Errorf("期望 '没有可用的技能。', 实际 %q", def.Description)
	}
}

func TestSkillDispatcher_GetDefinition_WithSkills(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	writeSkillDir(t, homeDir, "bar", "做bar")
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	def := dispatcher.GetDefinition()

	// 不应残留任何模板语法
	for _, bad := range []string{"{{", "}}", "{{range", "{{.Name}}", "{{.Skills}}", "{{end}}"} {
		if strings.Contains(def.Description, bad) {
			t.Errorf("description 中残留模板语法 %q: %q", bad, def.Description)
		}
	}
	// 两个技能都应被列出，且格式为 "- name: desc"
	if !strings.Contains(def.Description, "- foo: 做foo") {
		t.Errorf("期望包含 '- foo: 做foo', 实际 %q", def.Description)
	}
	if !strings.Contains(def.Description, "- bar: 做bar") {
		t.Errorf("期望包含 '- bar: 做bar', 实际 %q", def.Description)
	}
}

func TestSkillDispatcher_GetDefinition_NoAccumulation(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	first := dispatcher.GetDefinition().Description
	second := dispatcher.GetDefinition().Description

	// 多次调用不应导致描述累积变长
	if first != second {
		t.Errorf("多次调用结果不一致（疑似累积）:\n第一次=%q\n第二次=%q", first, second)
	}
	// 前缀只应出现一次
	if cnt := strings.Count(first, "技能分发器，支持以下技能:"); cnt != 1 {
		t.Errorf("期望前缀出现 1 次, 实际 %d 次", cnt)
	}
}

func TestSkillDispatcher_GetDefinition_DeterministicOrder(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "zeta", "z")
	writeSkillDir(t, homeDir, "alpha", "a")
	writeSkillDir(t, homeDir, "mike", "m")
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	a := dispatcher.GetDefinition().Description
	b := dispatcher.GetDefinition().Description
	if a != b {
		t.Errorf("多次调用顺序不一致:\n第一次=%q\n第二次=%q", a, b)
	}
	// 按名称排序后, alpha 应出现在 mike 之前、mike 在 zeta 之前
	ai := strings.Index(a, "- alpha")
	mi := strings.Index(a, "- mike")
	zi := strings.Index(a, "- zeta")
	if !(ai < mi && mi < zi) {
		t.Errorf("期望按名称排序 alpha<mike<zeta, 实际索引 %d/%d/%d", ai, mi, zi)
	}
}

func TestSkillDispatcher_DispatchSkill_Found(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	skill, err := dispatcher.DispatchSkill("foo")
	if err != nil {
		t.Fatalf("期望找到技能, 实际错误: %v", err)
	}
	if skill.Name != "foo" || skill.Description != "做foo" {
		t.Errorf("返回的技能字段不符: %+v", skill)
	}
}

func TestSkillDispatcher_DispatchSkill_NotFound(t *testing.T) {
	homeDir := t.TempDir()
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	_, err := dispatcher.DispatchSkill("nope")
	if err == nil {
		t.Fatal("期望技能不存在时返回错误")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestSkillDispatcher_Execute(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	loader := NewSkillLoader(homeDir, t.TempDir())
	dispatcher := NewSkillDispatcher(loader)

	input, _ := json.Marshal(map[string]interface{}{"skill_name": "foo"})
	out, err := dispatcher.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "[已加载技能 foo]") {
		t.Errorf("期望以[已加载技能 foo]开头, 实际 %q", out)
	}
	// 技能正文必须随工具结果回到上下文，否则模型只知道"分发成功"而无从遵循技能约定。
	if !strings.Contains(out, "body") {
		t.Errorf("期望包含技能正文, 实际 %q", out)
	}

	// 不存在的技能应返回错误
	badInput, _ := json.Marshal(map[string]interface{}{"skill_name": "missing"})
	if _, err := dispatcher.Execute(context.Background(), badInput); err == nil {
		t.Error("期望分发不存在技能时返回错误")
	}
}

// writeSkillResource 在技能目录下写入指定路径的资源文件。
func writeSkillResource(t *testing.T, base, skillName, relPath string) {
	t.Helper()
	path := filepath.Join(base, ".config/code-agent-go/skills", skillName, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建资源目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatalf("写入资源文件失败: %v", err)
	}
}

// TestSkillDispatcher_Execute_Idempotent 验证重复分发同一技能只注入一次正文：
// 正文进入上下文后就一直在那里，重复注入只会挤占窗口。
func TestSkillDispatcher_Execute_Idempotent(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	writeSkillResource(t, homeDir, "foo", "references/api.md")
	dispatcher := NewSkillDispatcher(NewSkillLoader(homeDir, t.TempDir()))
	input, _ := json.Marshal(map[string]interface{}{"skill_name": "foo"})

	first, err := dispatcher.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("第一次执行失败: %v", err)
	}
	if !strings.Contains(first, "[已加载技能 foo]") {
		t.Errorf("首次应注入正文, 实际 %q", first)
	}

	second, err := dispatcher.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("第二次执行失败: %v", err)
	}
	if strings.Contains(second, "[已加载技能 foo]") {
		t.Errorf("重复加载不应再注入正文, 实际 %q", second)
	}
	if !strings.Contains(second, "无需重复加载") {
		t.Errorf("重复加载应给出简短回执, 实际 %q", second)
	}
	// 回执里仍保留资源索引（很短且有用）。
	if !strings.Contains(second, "references/api.md") {
		t.Errorf("重复加载回执应保留资源索引, 实际 %q", second)
	}
}

// TestSkillDispatcher_Execute_Concurrent 验证并发重复分发时只有一次真正注入正文。
func TestSkillDispatcher_Execute_Concurrent(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	dispatcher := NewSkillDispatcher(NewSkillLoader(homeDir, t.TempDir()))
	input, _ := json.Marshal(map[string]interface{}{"skill_name": "foo"})

	const n = 4
	results := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			out, err := dispatcher.Execute(context.Background(), input)
			if err != nil {
				t.Errorf("执行失败: %v", err)
				return
			}
			results[idx] = out
		}(i)
	}
	wg.Wait()

	injected := 0
	for _, out := range results {
		if strings.Contains(out, "[已加载技能 foo]") {
			injected++
		}
		// 所有调用都必须拿到非空且有意义的结果，不能返回空串让模型卡住。
		if out == "" {
			t.Fatal("存在空的技能结果")
		}
	}
	if injected != 1 {
		t.Errorf("期望仅 1 次注入正文, 实际 %d 次", injected)
	}
}

// TestSkillDispatcher_MaxOutputSize 验证技能正文使用独立输出预算，
// 不被面向命令/文件输出的 20KB 通用上限切掉。
func TestSkillDispatcher_MaxOutputSize(t *testing.T) {
	dispatcher := NewSkillDispatcher(NewSkillLoader(t.TempDir(), t.TempDir()))
	if got := dispatcher.MaxOutputSize(); got != skillMaxOutputSize || got <= 20*1024 {
		t.Errorf("技能输出预算应高于通用 20KB 上限, 实际 %d", got)
	}
}

// TestSkillDispatcher_GetDefinition_UsageHint 验证描述里带了用法说明：
// 模型需要知道"技能要先加载"，否则可能直接按名字瞎猜。
func TestSkillDispatcher_GetDefinition_UsageHint(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	dispatcher := NewSkillDispatcher(NewSkillLoader(homeDir, t.TempDir()))

	def := dispatcher.GetDefinition()
	if !strings.Contains(def.Description, "用法:") {
		t.Errorf("期望包含用法说明, 实际 %q", def.Description)
	}
	if !strings.Contains(def.Description, "skill_name") {
		t.Errorf("用法说明应指明传入 skill_name, 实际 %q", def.Description)
	}
}

// TestSkillDispatcher_Execute_ResourceIndex 验证资源索引随正文一起交付：
// 正文里通常只说明做法，具体素材在 references/ scripts/ assets/ 下，
// 模型需要拿到索引才知道下一步能读什么。
func TestSkillDispatcher_Execute_ResourceIndex(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	writeSkillResource(t, homeDir, "foo", "references/api.md")
	writeSkillResource(t, homeDir, "foo", "scripts/run.sh")
	writeSkillResource(t, homeDir, "foo", "assets/template.txt")

	dispatcher := NewSkillDispatcher(NewSkillLoader(homeDir, t.TempDir()))
	input, _ := json.Marshal(map[string]interface{}{"skill_name": "foo"})

	out, err := dispatcher.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	skillDir := filepath.Join(homeDir, ".config/code-agent-go/skills", "foo")
	for _, want := range []string{
		"references/\n- api.md (" + filepath.Join(skillDir, "references/api.md") + ")",
		"scripts/\n- run.sh (" + filepath.Join(skillDir, "scripts/run.sh") + ")",
		"assets/\n- template.txt (" + filepath.Join(skillDir, "assets/template.txt") + ")",
		"技能目录: " + skillDir,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("期望资源索引包含 %q, 实际输出:\n%s", want, out)
		}
	}
}

// TestSkillDispatcher_Execute_NoResourceSection 验证没有资源时不输出空的资源小节。
func TestSkillDispatcher_Execute_NoResourceSection(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")

	dispatcher := NewSkillDispatcher(NewSkillLoader(homeDir, t.TempDir()))
	input, _ := json.Marshal(map[string]interface{}{"skill_name": "foo"})

	out, err := dispatcher.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if strings.Contains(out, "该技能附带以下资源") {
		t.Errorf("无资源时不该出现资源索引段落, 实际 %q", out)
	}
}

// TestSkillDispatcher_IndexFallsBackToRelative 验证技能没有 BaseDir 时，
// 索引退回相对路径输出（不能因为缺 BaseDir 就什么都不给）。
func TestSkillDispatcher_IndexFallsBackToRelative(t *testing.T) {
	skill := &schema.Skill{
		Name:        "foo",
		Description: "做foo",
		Body:        "body",
		References:  []schema.Reference{{Name: "api.md", Path: "references/api.md"}},
	}
	index := formatResourceIndex(skill)
	if want := "- api.md (references/api.md)"; !strings.Contains(index, want) {
		t.Errorf("无 BaseDir 时应输出相对路径 %q, 实际:\n%s", want, index)
	}
	if strings.Contains(index, "技能目录:") {
		t.Errorf("无 BaseDir 时不应输出技能目录行, 实际:\n%s", index)
	}
}
