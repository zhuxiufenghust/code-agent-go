package provider

import (
	"context"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// stubStreamProvider 内嵌 UsageTracker，只实现 GenerateStream：
// 每次调用按 script 顺序下发一个 done chunk（携带用量）。
type stubStreamProvider struct {
	UsageTracker
	script []*schema.Usage
	idx    int
}

// Generate 转调统一的 GenerateFromStream：这是各 Provider 实现非流式调用的标准姿势。
func (p *stubStreamProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return GenerateFromStream(ctx, p.GenerateStream, msgs, tools)
}

func (p *stubStreamProvider) GenerateStream(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk, 2)
	u := &schema.Usage{}
	if p.idx < len(p.script) {
		u = p.script[p.idx]
	}
	p.idx++
	defer p.AccumulateUsage(u)
	go func() {
		defer close(ch)
		ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: "ok"}
		ch <- schema.StreamChunk{
			Type:    schema.StreamChunkDone,
			Message: &schema.Message{Role: schema.AssistantRole, Content: "ok"},
			Usage:   u,
		}
	}()
	return ch, nil
}

// TestUsageTracker_GetUsageAccumulates 验证用量会跨多次调用累计（供上层做 token 统计）。
func TestUsageTracker_GetUsageAccumulates(t *testing.T) {
	p := &stubStreamProvider{script: []*schema.Usage{
		{InputTokens: 10, OutputTokens: 4},
		{InputTokens: 20, OutputTokens: 6},
	}}

	for i := 0; i < 2; i++ {
		msg, usage, err := p.Generate(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("第 %d 次 Generate 失败: %v", i+1, err)
		}
		if msg == nil || usage == nil {
			t.Fatalf("第 %d 次 Generate 应返回消息与用量", i+1)
		}
	}

	got := p.GetUsage()
	if got.InputTokens != 30 || got.OutputTokens != 10 {
		t.Fatalf("期望累计用量 {30,10}, 实际 %+v", got)
	}
}

// TestGenerateFromStream 验证统一的非流式实现：
// 成功时返回 done chunk 的消息与用量；流内报错时透传错误；建流失败时透传错误。
func TestGenerateFromStream(t *testing.T) {
	doneMsg := &schema.Message{Role: schema.AssistantRole, Content: "ok"}
	doneUsage := &schema.Usage{InputTokens: 7, OutputTokens: 3}

	msg, usage, err := GenerateFromStream(context.Background(), func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
		ch := make(chan schema.StreamChunk, 2)
		ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: "ok"}
		ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: doneMsg, Usage: doneUsage}
		close(ch)
		return ch, nil
	}, nil, nil)
	if err != nil {
		t.Fatalf("成功路径不应返回错误: %v", err)
	}
	if msg != doneMsg || usage != doneUsage {
		t.Fatalf("应原样返回 done chunk 的消息与用量, 实际 msg=%v usage=%v", msg, usage)
	}

	// 流内 error chunk
	if _, _, err = GenerateFromStream(context.Background(), func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
		ch := make(chan schema.StreamChunk, 1)
		ch <- schema.StreamChunk{Type: schema.StreamChunkError, Err: context.DeadlineExceeded}
		close(ch)
		return ch, nil
	}, nil, nil); err == nil {
		t.Fatal("流内 error chunk 应被透传")
	}

	// 建流失败
	if _, _, err = GenerateFromStream(context.Background(), func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
		return nil, context.Canceled
	}, nil, nil); err == nil {
		t.Fatal("建流失败应被透传")
	}

	// 未传入建流函数：明确报错而不是 panic
	if _, _, err = GenerateFromStream(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("streamFn 为 nil 时应返回错误")
	}
}

// TestUsageTracker_GetUsageZeroOnError 验证流出错时不会累计用量（避免脏数据）。
func TestUsageTracker_GetUsageZeroOnError(t *testing.T) {
	p := &errStreamProvider{}
	if _, _, err := p.Generate(context.Background(), nil, nil); err == nil {
		t.Fatal("期望 Generate 返回错误")
	}
	if got := p.GetUsage(); got.InputTokens != 0 || got.OutputTokens != 0 {
		t.Fatalf("出错时不应累计用量, 实际 %+v", got)
	}
}

// errStreamProvider 只在第一帧下发 error chunk。
type errStreamProvider struct {
	UsageTracker
}

func (p *errStreamProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return GenerateFromStream(ctx, p.GenerateStream, msgs, tools)
}

func (p *errStreamProvider) GenerateStream(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk, 1)
	ch <- schema.StreamChunk{Type: schema.StreamChunkError, Err: context.Canceled}
	close(ch)
	return ch, nil
}

// TestOpenAIProvider_GenerateDispatchesToOwnStream 是回归测试：
// OpenAIProvider.Generate 必须走到自己的 GenerateStream，
// 而不是退化成被嵌入类型上的空实现（嵌入无虚派发的经典坑）。
func TestOpenAIProvider_GenerateDispatchesToOwnStream(t *testing.T) {
	var _ LLMProvider = (*OpenAIProvider)(nil)

	// 指向不可达地址：期望拿到"请求失败"的具体错误，而不是 panic("not implemented")。
	t.Setenv("TEST_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("TEST_API_KEY", "test-key")
	p := NewOpenAIProvider(&config.OpenAIConfig{
		Model:      "test",
		BaseURLEnv: "TEST_BASE_URL",
		ApiKeyEnv:  "TEST_API_KEY",
		Options:    config.OpenAIOptions{MaxRetries: 1},
	})
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Generate 不应 panic, 实际: %v", rec)
		}
	}()
	if _, _, err := p.Generate(context.Background(), nil, nil); err == nil {
		t.Skip("环境可直连该地址，跳过断言")
	}
}
