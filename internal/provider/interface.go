package provider

import (
	"context"
	"errors"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type LLMProvider interface {

	// Generate 将对话历史和可用工具定义发送给 LLM，返回模型的完整响应 Message 和 token 用量。
	//
	// 参数:
	//   - ctx: 控制底层 HTTP 调用的取消和超时
	//   - messages: 完整的对话上下文，包含 system prompt、之前的 user/assistant 消息、
	//     以及工具 Observation
	//   - availableTools: 当前 Turn 中模型可调用的工具定义列表；
	//     传入 nil 剥夺所有工具（Phase 1 Thinking），传入非空恢复工具（Phase 2 Action）
	//
	// 返回的 Message 中 ToolCalls 字段在模型决定调用工具时填充；否则 Content 包含
	// 最终文本回复，agent loop 终止。Usage 包含本次调用的实际 token 用量（可能为 nil）。
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)

	// GenerateStream 以流式方式调用 LLM，通过 channel 逐 chunk 返回响应增量。
	//
	// 参数与 Generate 完全一致。返回的 channel 中每个 chunk 代表 LLM 的一次增量产出：
	//   - StreamChunkTextDelta:     文本增量（逐 token）
	//   - StreamChunkThinkingDelta: 推理增量（逐 token，仅支持 thinking 的模型）
	//   - StreamChunkDone:          流结束，携带完整的 Message
	//   - StreamChunkError:         出错
	//
	// 返回的 channel 会在流结束时自动关闭。调用方必须从 channel 读取直到关闭，
	// 以确保底层 HTTP 连接被正确释放。
	GenerateStream(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error)
}

// 通过BaseProvider实现LLMProvider接口的默认方法，Generate调用GenerateStream并收集结果
// 具体的GenerateStream方法需要在具体的Provider中实现
type BaseProvider struct {
	// 这里可以放一些通用的字段，比如配置、日志等
}

func (p *BaseProvider) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	ch, err := p.GenerateStream(ctx, messages, availableTools)
	if err != nil {
		return nil, nil, err
	}
	var finalMessage *schema.Message
	var usage *schema.Usage
	for chunk := range ch {
		switch chunk.Type {
		case schema.StreamChunkDone:
			finalMessage = chunk.Message
			usage = chunk.Usage
			return finalMessage, usage, nil
		case schema.StreamChunkError:
			return nil, nil, chunk.Err
		}
	}
	return nil, nil, errors.New("stream closed without final message")
}

func (p *BaseProvider) GenerateStream(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	panic("not implemented")
}

func sendStreamChunk(ctx context.Context, ch chan<- schema.StreamChunk, chunk schema.StreamChunk) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- chunk:
		return true
	}
}
