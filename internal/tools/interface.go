package tools

import (
	"context"
	"encoding/json"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type Tool interface {
	GetDefinition() schema.ToolDefinition
	// Execute 执行工具的主要逻辑，接收输入参数并返回结果或错误。
	//
	// 参数:
	// - ctx: 上下文对象，用于控制执行的生命周期和取消操作。
	// - input: 工具的输入参数，通常为 JSON 格式的原始消息。
	//	返回值:
	// - string: 工具执行的输出结果，通常为 JSON 格式的字符串。
	// - error: 执行过程中可能发生的错误，如果没有错误则返回 nil。
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}
