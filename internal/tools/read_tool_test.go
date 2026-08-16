package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	return path
}

func TestNewReadTool_Definition(t *testing.T) {
	tool := NewReadTool()
	def := tool.GetDefinition()
	if def.Name != "read_tool" {
		t.Errorf("期望 tool name 为 read_tool, 实际为 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	if def.InputSchema == nil {
		t.Error("期望 input_schema 非空")
	}
}

func TestReadTool_EmptyFilePath(t *testing.T) {
	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"file_path": ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 file_path 为空时返回错误")
	}
	if !strings.Contains(err.Error(), "file_path 参数不能为空") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestReadTool_InvalidJSON(t *testing.T) {
	tool := NewReadTool()
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"file_path": "/no/such/file.txt"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望文件不存在时返回错误")
	}
}

func TestReadTool_ReadByOffset(t *testing.T) {
	content := "hello world"
	path := writeTempFile(t, content)

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"file_path": path})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != content {
		t.Errorf("期望 %q, 实际 %q", content, got)
	}
}

func TestReadTool_ReadByOffsetWithLimit(t *testing.T) {
	path := writeTempFile(t, "abcdefghij")

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": path,
		"offset":    2,
		"limit":     100,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != "cdefghij" {
		t.Errorf("期望 %q, 实际 %q", "cdefghij", got)
	}
}

func TestReadTool_OffsetExceedsFileSize(t *testing.T) {
	path := writeTempFile(t, "short")

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": path,
		"offset":    100,
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 offset 超出文件大小时返回错误")
	}
	if !strings.Contains(err.Error(), "偏移量超出文件大小") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestReadTool_TruncationByLimit(t *testing.T) {
	path := writeTempFile(t, strings.Repeat("x", 100))

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": path,
		"limit":     10,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(got, "内容已截断") {
		t.Errorf("期望返回截断提示, 实际 %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) {
		t.Errorf("期望以 10 个 x 开头, 实际 %q", got)
	}
}

func TestReadTool_ReadByLines(t *testing.T) {
	lines := []string{"first", "second", "third", "fourth"}
	path := writeTempFile(t, strings.Join(lines, "\n"))

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":  path,
		"start_line": 2,
		"end_line":   3,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	want := "     2\tsecond\n     3\tthird\n"
	if got != want {
		t.Errorf("期望 %q, 实际 %q", want, got)
	}
}

func TestReadTool_ReadByLinesToEnd(t *testing.T) {
	lines := []string{"a", "b", "c"}
	path := writeTempFile(t, strings.Join(lines, "\n"))

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":  path,
		"start_line": 2,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	want := "     2\tb\n     3\tc\n"
	if got != want {
		t.Errorf("期望 %q, 实际 %q", want, got)
	}
}
