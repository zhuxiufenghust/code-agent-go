package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type BashTool struct {
	schema.ToolDefinition
	workDir      string
	timeout      time.Duration // 单条命令超时；0 时使用 defaultBashTimeout
	maxOutputLen int           // 最大输出长度，防止内存溢出 默认 0 时不限制
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

	return t.runLocal(ctx, params.Command)
}

func (t *BashTool) runLocal(ctx context.Context, cmd string) (string, error) {
	tmp, err := os.CreateTemp("", "harness9-bash-*.log")
	if err != nil {
		return "", fmt.Errorf("创建临时输出文件失败：%w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Dir = t.workDir
	c.Stdout = tmp
	c.Stderr = tmp
	err = c.Run()
	if err != nil {
		return "", fmt.Errorf("执行命令失败：%w", err)
	}
	out, readErr := os.ReadFile(tmp.Name())
	if readErr != nil {
		return "", fmt.Errorf("读取命令输出失败：%w", readErr)
	}
	if t.maxOutputLen > 0 {
		if len(out) > t.maxOutputLen {
			out = out[:t.maxOutputLen]
		}
	}
	if len(out) == 0 {
		out = []byte("命令执行成功，无终端输出。")
	}
	return string(out), nil
}
