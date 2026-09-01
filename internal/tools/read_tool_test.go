package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewReadTool_Definition(t *testing.T) {
	tool := NewReadTool(t.TempDir())
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
	tool := NewReadTool(t.TempDir())
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
	tool := NewReadTool(t.TempDir())
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	tool := NewReadTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"file_path": "no_such_file.txt"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望文件不存在时返回错误")
	}
}

func TestReadTool_ReadByOffset(t *testing.T) {
	dir := writeTempFileInDir(t, "test.txt", "hello world")
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{"file_path": "test.txt"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != "hello world" {
		t.Errorf("期望 %q, 实际 %q", "hello world", got)
	}
}

func TestReadTool_ReadByOffsetWithLimit(t *testing.T) {
	dir := writeTempFileInDir(t, "test.txt", "abcdefghij")
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "test.txt",
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
	dir := writeTempFileInDir(t, "test.txt", "short")
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "test.txt",
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
	dir := writeTempFileInDir(t, "test.txt", strings.Repeat("x", 100))
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "test.txt",
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
	dir := writeTempFileInDir(t, "test.txt", strings.Join(lines, "\n"))
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":  "test.txt",
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
	dir := writeTempFileInDir(t, "test.txt", strings.Join(lines, "\n"))
	tool := NewReadTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":  "test.txt",
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
