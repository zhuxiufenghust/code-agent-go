package tools

import (
	"context"
	"fmt"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

// MiddlewareFunc 定义了中间件的签名。
// 它接收当前的 ToolCall，并返回一个是否允许执行的布尔值 (allowed)，以及拦截时的原因 (rejectReason)。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (schema.ApprovalResult, error)

type Registry interface {
	// Register 将一个 BaseTool 实现注册到工具表中。
	// 若已存在同名工具，返回 error；原有工具保持不变。
	// 调用方需根据 error 决定是替换、忽略还是终止启动。
	Register(tool Tool) error

	// GetAvailableTools 返回所有已注册工具的 ToolDefinition 列表，
	// 供 LLM 在 Generate 调用时了解可用工具集。
	GetAvailableTools() []schema.ToolDefinition

	Use(mw MiddlewareFunc)

	// Execute 根据 ToolCall 中的工具名称查找并执行对应工具，
	// 返回封装后的 ToolResult（包含输出或错误信息）。
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

// defaultMaxToolOutput 是单个工具结果回灌上下文前的截断上限(字节)。
// 超出后仅保留前缀并附截断提示,避免大输出撑爆上下文窗口、放大 token 成本。
const defaultMaxToolOutput = 20 * 1024

// ToolOutputBudget 是工具可选实现的能力接口：声明自身输出回灌上下文时的
// 截断上限(字节)，优先于注册表全局的 maxOutputSize。
// 存在理由：不同工具的合理输出体量不同。命令/文件输出适合较小的通用上限，
// 而技能正文是成文的规范说明，天然更长，被通用上限切掉会让模型收到残缺的流程说明。
type ToolOutputBudget interface {
	MaxOutputSize() int
}

type registryImpl struct {
	tools         map[string]Tool
	maxOutputSize int
	middlewares   []MiddlewareFunc // 【新增】保存挂载的中间件链
}

// WithMaxOutputSize 设置工具输出截断上限(字节),覆盖默认值 defaultMaxToolOutput。
func WithMaxOutputSize(size int) RegistryOption {
	return func(r *registryImpl) {
		if size > 0 {
			r.maxOutputSize = size
		}
	}
}

// RegistryOption 是 registry 的可选配置项。
type RegistryOption func(*registryImpl)

// NewRegistry 创建一个新的工具注册表实例。
func NewRegistry(options ...RegistryOption) Registry {
	r := &registryImpl{
		tools:         make(map[string]Tool),
		maxOutputSize: defaultMaxToolOutput,
		middlewares:   make([]MiddlewareFunc, 0),
	}
	for _, opt := range options {
		opt(r)
	}
	return r
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
func (r *registryImpl) Use(mw MiddlewareFunc) {
	// 这里可以将中间件添加到中间件链中
	// 例如：
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	tool, exists := r.tools[call.Name]
	if !exists {
		return schema.ToolResult{
			ToolCallID: call.ID,
			IsError:    true,
			Output:     fmt.Sprintf("tool %s not found", call.Name),
			Name:       call.Name,
		}
	}

	var result schema.ToolResult
	defer func() {
		if rec := recover(); rec != nil {
			result = schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("Error: 工具 '%s' 执行时发生 panic: %v", call.Name, rec),
				IsError:    true,
				Name:       call.Name,
			}
		}
	}()

	for _, mw := range r.middlewares {
		result, err := mw(ctx, call)
		if err != nil {
			return schema.ToolResult{
				ToolCallID: call.ID,
				IsError:    true,
				Output:     fmt.Sprintf("tool %s rejected by middleware: %v", call.Name, err),
				Name:       call.Name,
			}
		}
		if !result.Allowed {
			return schema.ToolResult{
				ToolCallID: call.ID,
				IsError:    true,
				Output:     fmt.Sprintf("tool %s rejected by middleware: %s", call.Name, result.Reason),
				Name:       call.Name,
			}
		}
	}

	limit := r.resolveMaxOutputSize(tool)
	output, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		result = schema.ToolResult{
			ToolCallID: call.ID,
			IsError:    true,
			Output:     truncateToolOutput(call.Name, err.Error(), limit),
			Name:       call.Name,
		}
	} else {
		result = schema.ToolResult{
			ToolCallID: call.ID,
			Output:     truncateToolOutput(call.Name, output, limit),
			IsError:    false,
			Name:       call.Name,
		}
	}
	return result
}

// resolveMaxOutputSize 决定某个工具的输出上限：
// 工具自行声明优先（ToolOutputBudget），否则用注册表全局值。
func (r *registryImpl) resolveMaxOutputSize(tool Tool) int {
	if budgeted, ok := tool.(ToolOutputBudget); ok {
		if size := budgeted.MaxOutputSize(); size > 0 {
			return size
		}
	}
	return r.maxOutputSize
}

// truncateToolOutput 对工具输出做上限截断,并按工具类型给出差异化的"分段/过滤"建议。
// 截断对所有工具统一生效,但提示文案需针对工具语义,避免对 bash 输出给出"用 read 分段查看"这类无效建议。
func truncateToolOutput(toolName, out string, maxSize int) string {
	if len(out) <= maxSize {
		return out
	}
	kept := out[:maxSize]
	return fmt.Sprintf("%s\n...[已截断,共 %d 字节]\n%s", kept, len(out), truncationHint(toolName))
}

// truncationHint 返回与工具语义匹配的截断后操作建议。
func truncationHint(toolName string) string {
	switch toolName {
	case "read_tool":
		return "输出过长,请用 start_line/end_line 或 offset/limit 重新读取所需片段。"
	case "bash_tool":
		return "命令输出过长,建议用 head/tail/grep/sed 过滤,或将输出重定向到文件后用 read 分段查看。"
	case "web_fetch_tool", "web_search_tool":
		return "抓取/检索结果过长,建议缩小范围或改用更精确的 query。"
	case "skill_dispatcher":
		return "技能正文过长已截断:正文应只保留主流程步骤,把详细规则/模板拆到技能的 references/ 下,再用 read 按需读取。"
	default:
		return "输出过长,请缩小请求范围或分多次获取。"
	}
}
