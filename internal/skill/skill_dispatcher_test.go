package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	// GetDefinition 内部调用 log.Info，测试环境需先初始化 zap logger，
	// 否则会因 logger 为 nil 而 panic。
	cfg := zap.NewDevelopmentConfig()
	log.NewLogger(&cfg)
	os.Exit(m.Run())
}

// writeSkillDir 在 base 下创建一个名为 name 的技能目录，并写入合法的 SKILL.md。
func writeSkillDir(t *testing.T, base, name, desc string) {
	t.Helper()
	dir := filepath.Join(base, name)
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
	if !strings.Contains(out, "技能 foo 已成功分发") {
		t.Errorf("期望分发成功摘要, 实际 %q", out)
	}

	// 不存在的技能应返回错误
	badInput, _ := json.Marshal(map[string]interface{}{"skill_name": "missing"})
	if _, err := dispatcher.Execute(context.Background(), badInput); err == nil {
		t.Error("期望分发不存在技能时返回错误")
	}
}
