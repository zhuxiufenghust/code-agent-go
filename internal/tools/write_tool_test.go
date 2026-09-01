package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWriteTool_Definition(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	def := tool.GetDefinition()
	if def.Name != "write_tool" {
		t.Errorf("期望 tool name 为 write_tool, 实际为 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	if def.InputSchema == nil {
		t.Error("期望 input_schema 非空")
	}
}

func TestWriteTool_InvalidJSON(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestWriteTool_EmptyPath(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "",
		"content":   "data",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 file_path 为空时返回错误")
	}
}

func TestWriteTool_EmptyContent(t *testing.T) {
	tool := NewWriteTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "a.txt",
		"content":   "",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 content 为空时返回错误")
	}
}

func TestWriteTool_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	content := "line1\nline2\nline3\n"
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "new.txt",
		"content":   content,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "成功写入文件") {
		t.Errorf("期望返回成功摘要, 实际 %q", got)
	}

	written, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatalf("读取写入文件失败: %v", err)
	}
	if string(written) != content {
		t.Errorf("期望 %q, 实际 %q", content, string(written))
	}
}

func TestWriteTool_CreateNestedDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "a/b/c.txt",
		"content":   "nested",
	})
	if _, err := tool.Execute(context.Background(), input); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a/b/c.txt")); err != nil {
		t.Errorf("期望自动创建嵌套目录与文件: %v", err)
	}
}

func TestWriteTool_OverwriteFalseByDefault(t *testing.T) {
	dir := writeTempFileInDir(t, "exists.txt", "original")
	tool := NewWriteTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "exists.txt",
		"content":   "new",
	})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望文件已存在且未开启 overwrite 时返回错误")
	}
	if !strings.Contains(err.Error(), "文件已存在") {
		t.Errorf("错误信息不符合预期: %v", err)
	}

	// 原内容不应被改动
	written, _ := os.ReadFile(filepath.Join(dir, "exists.txt"))
	if string(written) != "original" {
		t.Errorf("期望原文件未被改动, 实际 %q", string(written))
	}
}

func TestWriteTool_OverwriteTrue(t *testing.T) {
	dir := writeTempFileInDir(t, "exists.txt", "original")
	tool := NewWriteTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path":  "exists.txt",
		"content":    "updated",
		"overwrite":  true,
	})
	if _, err := tool.Execute(context.Background(), input); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	written, _ := os.ReadFile(filepath.Join(dir, "exists.txt"))
	if string(written) != "updated" {
		t.Errorf("期望 updated, 实际 %q", string(written))
	}
}

func TestWriteTool_RelativeToWorkDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "rel.txt",
		"content":   "data",
	})
	if _, err := tool.Execute(context.Background(), input); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rel.txt")); err != nil {
		t.Errorf("期望在工作目录下创建文件: %v", err)
	}
}

func TestWriteTool_LineCountSummary(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)
	// 3 行 + 末尾换行，实际 3 行；无末尾换行时按 3 行计。
	input, _ := json.Marshal(map[string]interface{}{
		"file_path": "c.txt",
		"content":   "a\nb\nc",
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "共 3 行") || !strings.Contains(got, "5 字节") {
		t.Errorf("摘要行数/字节数不符合预期: %q", got)
	}
}
