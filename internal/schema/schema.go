package schema

import "encoding/json"

type Role string

const (
	UserRole      Role = "user"
	AssistantRole Role = "assistant"
	SystemRole    Role = "system"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
	// ToolCalls 仅在模型决定调用工具时填充；否则为 nil。
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	IsError    bool       `json:"is_error,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	// ToolCallID 镜像原始 ToolCall 的 ID，建立 LLM 期望的请求-响应关联。
	ToolCallID string `json:"tool_call_id"`

	// Output 包含工具执行捕获的 stdout/stderr 输出，或在工具失败时的错误堆栈信息。
	Output string `json:"output"`

	// Name 标记工具执行的名称，便于 LLM 识别调用来源。
	Name string `json:"name"`

	// IsError 标记工具执行是否失败。当为 true 时，引擎可将错误暴露给 LLM，
	// 使其尝试自愈（Self-Healing），例如修正命令语法后重试。
	IsError bool `json:"is_error"`
}

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
