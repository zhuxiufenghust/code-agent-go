package tools

import (
	"context"
	"fmt"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

type Registry interface {
	// Register 将一个 BaseTool 实现注册到工具表中。
	// 若已存在同名工具，返回 error；原有工具保持不变。
	// 调用方需根据 error 决定是替换、忽略还是终止启动。
	Register(tool Tool) error

	// GetAvailableTools 返回所有已注册工具的 ToolDefinition 列表，
	// 供 LLM 在 Generate 调用时了解可用工具集。
	GetAvailableTools() []schema.ToolDefinition

	// Execute 根据 ToolCall 中的工具名称查找并执行对应工具，
	// 返回封装后的 ToolResult（包含输出或错误信息）。
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	tools map[string]Tool
}

// NewRegistry 创建一个新的工具注册表实例。
func NewRegistry() Registry {
	return &registryImpl{
		tools: make(map[string]Tool),
	}
}

// var registry = &registryImpl{
// 	tools: make(map[string]Tool),
// }

func (r *registryImpl) Register(tool Tool) error {
	def := tool.GetDefinition()
	if def.Name == "" {
		log.Warn("tool def invalid", zap.String("tool_name", def.Name))
		return fmt.Errorf("tool def invalid")
	}
	if _, exists := r.tools[def.Name]; exists {
		log.Warn("tool already registered", zap.String("tool_name", def.Name))
		return fmt.Errorf("tool already registered")
	}

	r.tools[def.Name] = tool
	return nil
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	defs := make([]schema.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.GetDefinition())
	}
	return defs
}
func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	tool, exists := r.tools[call.Name]
	if !exists {
		return schema.ToolResult{
			ToolCallID: call.ID,
			IsError:    true,
			Output:     fmt.Sprintf("tool %s not found", call.Name),
		}
	}

	var result schema.ToolResult
	defer func() {
		if rec := recover(); rec != nil {
			result = schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("Error: 工具 '%s' 执行时发生 panic: %v", call.Name, rec),
				IsError:    true,
			}
		}
	}()

	output, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		result = schema.ToolResult{
			ToolCallID: call.ID,
			IsError:    true,
			Output:     err.Error(),
		}
	} else {
		result = schema.ToolResult{
			ToolCallID: call.ID,
			Output:     output,
			IsError:    false,
		}
	}
	return result
}
