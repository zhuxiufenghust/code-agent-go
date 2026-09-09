package prompt

import (
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

func TestBuildSystemPrompt_WorkDirSubstituted(t *testing.T) {
	tools := []schema.ToolDefinition{
		{Name: "edit_tool", Description: "编辑文件"},
	}
	got := BuildSystemPrompt("/my/work/dir", tools)
	if !strings.Contains(got, "/my/work/dir") {
		t.Errorf("期望包含 workDir, 实际 %q", got)
	}
}

func TestBuildSystemPrompt_NoTemplateLeftover(t *testing.T) {
	tools := []schema.ToolDefinition{
		{Name: "edit_tool", Description: "编辑文件"},
		{Name: "read_tool", Description: "读取文件"},
	}
	got := BuildSystemPrompt("/wd", tools)
	// 不应残留任何模板语法
	for _, bad := range []string{"{{", "}}", "{{range", "{{.Name}}", "{{.Tools}}", "{{end}}"} {
		if strings.Contains(got, bad) {
			t.Errorf("prompt 中残留模板语法 %q: %q", bad, got)
		}
	}
}

func TestBuildSystemPrompt_EachToolListedOnce(t *testing.T) {
	tools := []schema.ToolDefinition{
		{Name: "edit_tool", Description: "编辑文件"},
		{Name: "read_tool", Description: "读取文件"},
	}
	got := BuildSystemPrompt("/wd", tools)

	// 工具列表前缀只应出现一次
	if cnt := strings.Count(got, "你可以使用以下工具："); cnt != 1 {
		t.Errorf("期望工具列表前缀出现 1 次, 实际 %d 次", cnt)
	}
	// 每个工具都应以其名称+描述出现，且格式为 "- name: desc"
	for _, tool := range tools {
		line := "- " + tool.Name + ": " + tool.Description
		if !strings.Contains(got, line) {
			t.Errorf("期望包含 %q, 实际 %q", line, got)
		}
	}
}

func TestBuildSystemPrompt_EmptyTools(t *testing.T) {
	got := BuildSystemPrompt("/wd", nil)
	if !strings.Contains(got, "/wd") {
		t.Errorf("期望仍包含 workDir, 实际 %q", got)
	}
	// 空工具列表也不应残留模板语法
	if strings.Contains(got, "{{") {
		t.Errorf("空工具时不应残留模板语法: %q", got)
	}
}
