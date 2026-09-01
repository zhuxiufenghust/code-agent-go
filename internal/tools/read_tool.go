package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

const maxReadLen = 1024 * 1024 // 1MB
const maxLineRead = 500        // 最大读取行数限制，防止一次性读取过多行导致内存占用过高

type ReadTool struct {
	schema.ToolDefinition
	workDir string
}

type readToolInput struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
}
type ReadToolOption func(*ReadTool)

func NewReadTool(workDir string, options ...ReadToolOption) *ReadTool {
	t := &ReadTool{
		ToolDefinition: schema.ToolDefinition{
			Name: "read_tool",
			Description: "读取指定路径的文件内容。" +
				"推荐使用 start_line/end_line 按行号读取片段（最直观）：返回的每行带 `行号→Tab→内容` 前缀，便于定位。" +
				"⚠️ 行号前缀仅供显示，调用 edit_file 时 source_text 必须是不含行号前缀的原始代码。" +
				"也支持 offset/limit 字节偏移分页（单位为字节，不是行号）。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "要读取的文件的路径, 如 src/main.py",
					},
					"start_line": map[string]interface{}{
						"type":        "integer",
						"description": "从第 N 行开始读（1-based，含）。与 end_line 配合使用。设置后 offset 参数无效。",
					},
					"end_line": map[string]interface{}{
						"type":        "integer",
						"description": fmt.Sprintf("读到第 N 行结束（1-based，含）。需配合 start_line 使用；不设则读到文件末尾（上限 %d 行）。", maxLineRead),
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "读取的最大字节数。与 offset 配合使用。设置后 start_line/end_line 参数无效。",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": fmt.Sprintf("最多读取的字节数（默认 %d）。与 offset 配合使用；使用 start_line/end_line 时此参数无效。", maxReadLen),
					},
				},
				"required": []string{"file_path"},
			},
		},
	}
	t.workDir = workDir
	for _, opt := range options {
		opt(t)
	}

	return t
}

func (t *ReadTool) GetDefinition() schema.ToolDefinition {
	return t.ToolDefinition
}
func (t *ReadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params readToolInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("解析输入参数失败: %v", err)
	}

	if params.FilePath == "" {
		return "", fmt.Errorf("file_path 参数不能为空")
	}

	params.FilePath = filepath.Join(t.workDir, params.FilePath)
	content, err := readFileContent(params)
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %v", err)
	}
	return content, nil
}

func readFileByLines(fullPath string, startLine, endLine int) (string, error) {
	if startLine < 1 {
		startLine = 1
	}
	// endLine 0 表示"读到末尾"，但上限 maxLineRead 行
	if endLine <= 0 || endLine-startLine+1 > maxLineRead {
		endLine = startLine + maxLineRead - 1
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// 扩大 scanner buffer 以支持较长行
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	var content strings.Builder
	currentLine := 0
	for scanner.Scan() {
		currentLine++
		if currentLine < startLine {
			continue
		}
		if currentLine > endLine {
			break
		}
		content.WriteString(fmt.Sprintf("%6d\t%s\n", currentLine, scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return content.String(), nil
}

func readFileContent(params readToolInput) (string, error) {
	// 这里实现读取文件内容的逻辑，可以根据 startLine/endLine 或 offset/limit 来读取文件。
	// 例如：
	// - 如果 startLine 和 endLine 被设置，则按行读取文件内容。
	// - 如果 offset 和 limit 被设置，则按字节偏移读取文件内容。
	// 注意：需要处理文件不存在、权限不足等错误情况。
	if params.StartLine > 0 {
		return readFileByLines(params.FilePath, params.StartLine, params.EndLine)

	}
	return readFileByOffset(params.FilePath, params.Offset, params.Limit)
}

func readFileByOffset(fullPath string, offset int64, limit int) (string, error) {
	if limit <= 0 || limit > maxReadLen {
		limit = maxReadLen
	}
	if offset < 0 {
		offset = 0
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("获取文件信息失败: %w", err)
	}
	totalSize := info.Size()

	if offset > totalSize {
		return "", fmt.Errorf("偏移量超出文件大小")
	}

	_, err = file.Seek(offset, 0)
	if err != nil {
		return "", err
	}

	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %w", err)
	}
	if len(content) > limit {
		nextOffset := offset + int64(limit)
		return string(content[:limit]) + fmt.Sprintf(
			"\n\n...[内容已截断。offset 和 limit 单位均为字节（非行号）。"+
				"已读取 offset=%d 起的 %d 字节，文件总大小 %d 字节。"+
				"如需继续读取，请使用 offset=%d（不要把行号当作 offset）。"+
				"提示：按行读取更直观，可改用 start_line/end_line 参数。]...",
			offset, limit, totalSize, nextOffset,
		), nil
	}
	return string(content), nil
}
