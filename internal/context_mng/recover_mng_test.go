package context_mng

import (
	"context"
	"strings"
	"testing"
)

// TestRecovery_Hit 验证各工具的真实报错都能命中并注入 [系统救援指南]。
// 报错原文均取自 edit_tool.go / read_tool.go / write_tool.go 的真实 fmt.Errorf。
func TestRecovery_Hit(t *testing.T) {
	rm := NewRecoveryManager()
	cases := []struct {
		name string
		tool string
		err  string
	}{
		{"edit 未找到old_text", "edit_tool", "在文件中未找到 old_text，请大模型先调用 read_file 仔细确认文件内容和缩进"},
		{"edit 多处匹配", "edit_tool", "old_text 匹配到了 2 处，请提供更多的上下文代码以确保唯一性"},
		{"edit 模糊多处匹配", "edit_tool", "模糊匹配到了 2 处相似代码，请提供更多上下行代码以精确定位"},
		{"edit 文件不存在", "edit_tool", "文件不存在: /tmp/nope.go"},
		{"read 文件不存在", "read_tool", "读取文件内容失败: open /tmp/nope: no such file or directory"},
		{"write 无权限", "write_tool", "写入文件失败: open /etc/foo: permission denied"},
		{"bash 命令未安装", "bash_tool", "bash: xyz: command not found"},
		{"bash 超时", "bash_tool", "context deadline exceeded"},
		{"bash 语法错误", "bash_tool", "bash: syntax error near unexpected token"},
	}
	for _, c := range cases {
		out := rm.AnalyzeAndInject(context.Background(), c.tool, c.err)
		if !strings.Contains(out, "[系统救援指南]") {
			t.Errorf("[%s] 期望注入救援指南, 实际: %q", c.name, out)
		}
	}
}

// TestRecovery_MissReturnsRaw 验证未匹配到特征时原样返回原始错误。
func TestRecovery_MissReturnsRaw(t *testing.T) {
	rm := NewRecoveryManager()
	raw := "some unrelated error 12345"
	out := rm.AnalyzeAndInject(context.Background(), "edit_tool", raw)
	if out != raw {
		t.Errorf("未匹配时应原样返回, 实际: %q", out)
	}
}

// TestRecovery_UnknownToolNoHint 验证工具名不匹配（如历史遗留的 edit_file）时不生成 hint。
func TestRecovery_UnknownToolNoHint(t *testing.T) {
	rm := NewRecoveryManager()
	raw := "在文件中未找到 old_text，请先 read_file"
	out := rm.AnalyzeAndInject(context.Background(), "edit_file", raw) // 故意传错工具名
	if strings.Contains(out, "[系统救援指南]") {
		t.Errorf("工具名不匹配时不应生成 hint, 实际: %q", out)
	}
}
