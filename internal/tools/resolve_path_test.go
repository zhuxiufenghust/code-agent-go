package tools

import (
	"path/filepath"
	"testing"
)

func TestResolvePath(t *testing.T) {
	workDir := "/Users/admin/project/go_project/ai/code-agent-go"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"相对路径", "cmd/main.go", filepath.Join(workDir, "cmd/main.go")},
		{"绝对路径", "/Users/admin/project/go_project/ai/code-agent-go/cmd/main.go", "/Users/admin/project/go_project/ai/code-agent-go/cmd/main.go"},
		// 模型漏了前导 / 但内容已带 workDir 前缀：曾经会拼成 workDir/workDir/cmd/main.go
		{"漏前导斜杠且带workDir前缀", "Users/admin/project/go_project/ai/code-agent-go/cmd/main.go", filepath.Join(workDir, "cmd/main.go")},
		{"带多余斜杠前缀", "/Users/admin/project/go_project/ai/code-agent-go//cmd/main.go", "/Users/admin/project/go_project/ai/code-agent-go/cmd/main.go"},
	}
	for _, c := range cases {
		got := resolvePath(workDir, c.in)
		if got != c.want {
			t.Errorf("[%s] 期望 %q, 实际 %q", c.name, c.want, got)
		}
	}
}
