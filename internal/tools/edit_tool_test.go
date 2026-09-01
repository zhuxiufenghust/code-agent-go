package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempFileInDir 在临时目录中创建名为 name 的文件，返回该临时目录的绝对路径。
func writeTempFileInDir(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	return dir
}

func TestNewEditTool_Definition(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	def := tool.GetDefinition()
	if def.Name != "edit_tool" {
		t.Errorf("期望 tool name 为 edit_tool, 实际为 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	if def.InputSchema == nil {
		t.Error("期望 input_schema 非空")
	}
}

func TestEditTool_InvalidJSON(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestEditTool_FileNotFound(t *testing.T) {
	tool := NewEditTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "no_such_file.txt",
		"source_text": "a",
		"target_text": "b",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望文件不存在时返回错误")
	}
	if !strings.Contains(err.Error(), "文件不存在") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestEditTool_L1_ExactMatch(t *testing.T) {
	dir := writeTempFileInDir(t, "a.txt", "hello world")
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "hello",
		"target_text": "hi",
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "成功修改文件") {
		t.Errorf("期望返回成功摘要, 实际 %q", got)
	}

	written, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("读取回写文件失败: %v", err)
	}
	if string(written) != "hi world" {
		t.Errorf("期望 hi world, 实际 %q", string(written))
	}
}

func TestEditTool_L1_MultipleMatch(t *testing.T) {
	dir := writeTempFileInDir(t, "a.txt", "foo bar foo")
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "foo",
		"target_text": "baz",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 old_text 多次匹配时返回错误")
	}
	if !strings.Contains(err.Error(), "匹配到了") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestEditTool_L2_CRLFNormalization(t *testing.T) {
	dir := writeTempFileInDir(t, "a.txt", "line1\r\nline2\r\nline3")
	tool := NewEditTool(dir)
	// source 使用 \n，而文件内部是 \r\n
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "line2\nline3",
		"target_text": "line2\nlineX",
	})
	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("读取回写文件失败: %v", err)
	}
	// L2 归一化后，\r\n 会被转换为 \n 写回
	if string(written) != "line1\nline2\nlineX" {
		t.Errorf("期望 line1\\nline2\\nlineX, 实际 %q", string(written))
	}
}

func TestEditTool_L3_TrimSpace(t *testing.T) {
	dir := writeTempFileInDir(t, "a.txt", "prefix prefixed content suffix")
	tool := NewEditTool(dir)
	// source 带有首尾空白，而文件内没有，故必须靠 L3 TrimSpace 才能匹配
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "  prefixed content  ",
		"target_text": "new content",
	})
	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("读取回写文件失败: %v", err)
	}
	if string(written) != "prefix new content suffix" {
		t.Errorf("期望 prefix new content suffix, 实际 %q", string(written))
	}
}

// TestEditTool_L4 使用多行 source_text，确保 L1/L2/L3 均无法命中，
// 从而真正触发 L4 逐行去缩进匹配（单行 "x := 1" 会作为子串命中 L1/L3）。
func TestEditTool_L4_LineByLineIndent(t *testing.T) {
	content := "\tx := 1\n\tx := 2\n"
	dir := writeTempFileInDir(t, "a.txt", content)
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "x := 1\nx := 2",
		"target_text": "x := 10\nx := 20",
	})
	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("读取回写文件失败: %v", err)
	}
	// L4 将匹配到的原始行整体替换为 target_text（保留文件原有的末尾换行）
	if string(written) != "x := 10\nx := 20\n" {
		t.Errorf("期望 x := 10\\nx := 20\\n, 实际 %q", string(written))
	}
}

func TestEditTool_L4_NotFound(t *testing.T) {
	content := "\tx := 1\n\tx := 2\n"
	dir := writeTempFileInDir(t, "a.txt", content)
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "x := 9\nx := 8",
		"target_text": "x := 0\nx := 0",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望未找到代码片段时返回错误")
	}
	if !strings.Contains(err.Error(), "未找到") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestEditTool_L4_MultipleMatch(t *testing.T) {
	content := "\tx := 1\n\tx := 2\n\tx := 1\n\tx := 2\n"
	dir := writeTempFileInDir(t, "a.txt", content)
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "a.txt",
		"source_text": "x := 1\nx := 2",
		"target_text": "x := 10\nx := 20",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望多次模糊匹配时返回错误")
	}
	if !strings.Contains(err.Error(), "模糊匹配到了") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestEditTool_WithEditWorkDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sub.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	// 使用相对于 workDir 的路径
	tool := NewEditTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":   "sub.txt",
		"source_text": "data",
		"target_text": "updated",
	})
	_, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
}

func TestBuildEditSummary_LargeChange(t *testing.T) {
	var orig, next strings.Builder
	for i := 0; i < 30; i++ {
		orig.WriteString("a\n")
		next.WriteString("b\n")
	}
	summary := buildEditSummary("big.txt", orig.String(), next.String())
	if !strings.Contains(summary, "删除 30 行") || !strings.Contains(summary, "新增 30 行") {
		t.Errorf("期望报告大批量删除/新增, 实际 %q", summary)
	}
}

func TestBuildEditSummary_Context(t *testing.T) {
	orig := "ctx1\nold1\nold2\nctx2\n"
	next := "ctx1\nnew1\nnew2\nctx2\n"
	summary := buildEditSummary("f.txt", orig, next)
	if !strings.Contains(summary, "- old1") || !strings.Contains(summary, "+ new1") {
		t.Errorf("期望包含 diff 行, 实际 %q", summary)
	}
	if !strings.Contains(summary, "  ctx1") || !strings.Contains(summary, "  ctx2") {
		t.Errorf("期望包含上下文行, 实际 %q", summary)
	}
}

func TestFuzzyReplace_Exact(t *testing.T) {
	got, err := fuzzyReplace("abc", "b", "X")
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if got != "aXc" {
		t.Errorf("期望 aXc, 实际 %q", got)
	}
}
