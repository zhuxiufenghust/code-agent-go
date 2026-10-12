package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/sandbox"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type BashTool struct {
	schema.ToolDefinition
	workDir string
	timeout time.Duration // 单条命令超时；0 时使用 defaultBashTimeout
	// runner 决定命令到底跑在哪：宿主机（默认）还是一次性容器（见 sandbox 配置）。
	runner sandbox.Runner
	// maxOutputLen 最大输出长度，防止内存溢出 默认 0 时不限制
	maxOutputLen int
	// blockCommands 阻塞的命令列表，默认空列表
}
type bashInput struct {
	Command     string  `json:"command"`
	TimeoutSecs float64 `json:"timeout_secs,omitempty"`
}

const defaultBashTimeout = 120 * time.Second

// maxBashTimeout 是单次调用 timeout_secs 可请求的上限，防止 Agent 设置过长超时拖死整体预算。
const maxBashTimeout = 600 * time.Second

// BashOption 是 BashTool 的功能选项函数。
type BashOption func(*BashTool)

func WithMaxOutputLen(maxOutputLen int) BashOption {
	return func(t *BashTool) {
		t.maxOutputLen = maxOutputLen
	}
}

// WithRunner 替换命令执行体，用于在沙箱（容器）中运行命令。
// 默认使用 sandbox.Local：与宿主机直跑语义一致，便于测试与本地开发。
func WithRunner(r sandbox.Runner) BashOption {
	return func(t *BashTool) {
		if r != nil {
			t.runner = r
		}
	}
}

func NewBashTool(workDir string, options ...BashOption) *BashTool {
	t := &BashTool{
		ToolDefinition: schema.ToolDefinition{
			Name:        "bash_tool",
			Description: "在当前工作区执行任意的 bash 命令。支持链式命令 (如 &&)。返回标准输出 (stdout) 和标准错误 (stderr) 的合并内容。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "要执行的 bash 命令, 如 ls -l",
					},
					"timeout_secs": map[string]interface{}{
						"type":        "integer",
						"description": fmt.Sprintf("可选：本次命令的超时秒数（默认 %d，上限 %d）。运行较慢的测试套件或安装命令时可适当调大。", int(defaultBashTimeout.Seconds()), int(maxBashTimeout.Seconds())),
					},
				},
				"required": []string{"command"},
			},
		},
		workDir: workDir,
		runner:  sandbox.Local{},
	}
	for _, opt := range options {
		opt(t)
	}
	return t

}

// WithTimeout 设置 BashTool 的超时时间。
func WithTimeout(timeout time.Duration) BashOption {
	return func(t *BashTool) {
		t.timeout = timeout
	}
}

func (t *BashTool) GetDefinition() schema.ToolDefinition {
	return t.ToolDefinition
}

func (t *BashTool) effectiveTimeout(timeoutSecs float64) time.Duration {
	if timeoutSecs > 0 && timeoutSecs <= maxBashTimeout.Seconds() {
		return time.Duration(timeoutSecs) * time.Second
	}
	if t.timeout > 0 {
		return t.timeout
	}
	return defaultBashTimeout
}
func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params bashInput
	err := json.Unmarshal(input, &params)
	if err != nil {
		return "", fmt.Errorf("unmarshal input to bashInput failed: %w", err)
	}

	timeout := t.effectiveTimeout(params.TimeoutSecs)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := t.runner.Run(ctx, t.workDir, params.Command)
	if err != nil {
		return "", err
	}
	if t.maxOutputLen > 0 {
		if len(out) > t.maxOutputLen {
			out = out[:t.maxOutputLen]
		}
	}
	if out == "" {
		out = "命令执行成功，无终端输出。"
	}
	return out, nil
}
