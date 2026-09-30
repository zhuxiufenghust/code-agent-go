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

	GetUsage() schema.Usage
}

// UsageTracker 是可被各 Provider 内嵌的"用量统计"状态复用，
// 只负责累计用量并对外提供 GetUsage。
//
// 这里刻意不放任何需要派发的行为：Go 的嵌入没有虚派发，
// 写在被嵌入类型上的 Generate 只会调用到它自己的 GenerateStream，
// 结果不是多态而是必错的空壳。Generate / GenerateStream 请各 Provider 自行实现，
// 其中 Generate 用 GenerateFromStream 一行转调即可。
type UsageTracker struct {
	usage schema.Usage
}

// StreamFunc 是 GenerateStream 的函数签名，用于把"建流"能力作为参数传给 GenerateFromStream。
type StreamFunc func(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error)

// GenerateFromStream 是非流式 Generate 的统一实现：建流 → 收集最终结果。
//
// 各 Provider 只需在自己的 Generate 里一行转调本函数，即可保证所有 Provider 的
// 非流式语义完全一致（不必复制粘贴收集逻辑）：
//
//	func (p *XxxProvider) Generate(ctx context.Context, msgs []schema.Message,
//		tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
//		return GenerateFromStream(ctx, p.GenerateStream, msgs, tools)
//	}
//
// 之所以做成"包级函数 + 传入方法值"，而不是放在被嵌入的类型上当默认实现：
// Go 的嵌入没有虚派发，被嵌入类型上的 Generate 只会调用到它自己的 GenerateStream；
// 而 p.GenerateStream 作为方法值传进来时已经绑定了正确的接收者，派发一定指向具体实现。
func GenerateFromStream(ctx context.Context, streamFn StreamFunc,
	messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	if streamFn == nil {
		return nil, nil, errors.New("GenerateFromStream: streamFn 为 nil")
	}
	ch, err := streamFn(ctx, messages, availableTools)
	if err != nil {
		return nil, nil, err
	}
	return CollectStream(ch)
}

// CollectStream 从流式 channel 收集最终结果：
// 读到 done chunk 返回其 Message/Usage，读到 error chunk 返回错误，
// channel 提前关闭则返回 "stream closed without final message"。
func CollectStream(ch <-chan schema.StreamChunk) (*schema.Message, *schema.Usage, error) {
	for chunk := range ch {
		switch chunk.Type {
		case schema.StreamChunkDone:
			return chunk.Message, chunk.Usage, nil
		case schema.StreamChunkError:
			return nil, nil, chunk.Err
		}
	}
	return nil, nil, errors.New("stream closed without final message")
}

// AccumulateUsage 累计一次调用的用量，供 GetUsage 汇总统计。
func (p *UsageTracker) AccumulateUsage(u *schema.Usage) {
	if u == nil {
		return
	}
	p.usage.InputTokens += u.InputTokens
	p.usage.OutputTokens += u.OutputTokens
}

func (p *UsageTracker) GetUsage() schema.Usage {
	return p.usage
}

func sendStreamChunk(ctx context.Context, ch chan<- schema.StreamChunk, chunk schema.StreamChunk) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- chunk:
		return true
	}
}
