package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type WriteTool struct {
	schema.ToolDefinition
	workDir string
}
type WriteToolOption func(*WriteTool)

type writeInput struct {
	FilePath    string `json:"file_path"`
	Content     string `json:"content"`
	Overwrite   bool   `json:"overwrite,omitempty"`
}

func NewWriteTool(workDir string, options ...WriteToolOption) *WriteTool {
	tool := &WriteTool{
		ToolDefinition: schema.ToolDefinition{
			Name: "write_tool",
			Description: "将完整内容整体写入文件。用于创建新文件，或整体覆盖重写已有文件。" +
				"若文件已存在且 overwrite 为 false（默认），则会报错以防止误覆盖。" +
				"需要局部、精确地修改现有文件时，请改用 edit_tool。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "要写入的文件的路径, 如 src/main.py",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "要写入文件的完整内容（整体覆盖，而非追加）",
					},
					"overwrite": map[string]interface{}{
						"type":        "boolean",
						"description": "文件已存在时是否覆盖，默认 false（不覆盖，避免误写）",
					},
				},
				"required": []string{"file_path", "content"},
			},
		},
	}
	tool.workDir = workDir
	for _, opt := range options {
		opt(tool)
	}
	return tool
}

func (t *WriteTool) GetDefinition() schema.ToolDefinition {
	return t.ToolDefinition
}

func (t *WriteTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params writeInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("解析输入参数失败: %v", err)
	}

	if params.FilePath == "" {
		return "", fmt.Errorf("file_path 参数不能为空")
	}
	if params.Content == "" {
		return "", fmt.Errorf("content 参数不能为空")
	}

	fullPath := resolvePath(t.workDir, params.FilePath)

	// 防止误覆盖已有文件：除非显式 overwrite=true，否则已存在文件直接报错。
	if _, err := os.Stat(fullPath); err == nil && !params.Overwrite {
		return "", fmt.Errorf("文件已存在，未开启 overwrite 不会覆盖: %s（如需覆盖请设置 overwrite=true，或改用 edit_tool 做局部修改）", fullPath)
	}

	// 自动创建不存在的父目录。
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建父目录失败: %w", err)
	}

	if err := os.WriteFile(fullPath, []byte(params.Content), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	lineCount := strings.Count(params.Content, "\n")
	if len(params.Content) > 0 && !strings.HasSuffix(params.Content, "\n") {
		lineCount++
	}
	return fmt.Sprintf("成功写入文件: %s（共 %d 行，%d 字节）", fullPath, lineCount, len(params.Content)), nil
}
