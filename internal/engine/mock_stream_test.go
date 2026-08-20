package engine

import (
	"context"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

type mockProvider struct{}

func (m *mockProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return nil, nil, nil
}

func (m *mockProvider) GenerateStream(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk)
	go func() {
		defer close(ch)
		for _, s := range []string{"你", "好", "，", "这", "是", "流式", "输出"} {
			ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: s}
			time.Sleep(20 * time.Millisecond)
		}
		ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: &schema.Message{Content: "你好，这是流式输出"}}
	}()
	return ch, nil
}

func TestEngineStreamEmitsIncrementally(t *testing.T) {
	reg := tools.NewRegistry()
	eng := NewAgentEngine(&mockProvider{}, reg)
	ctx := context.Background()
	ch, err := eng.StreamRun(ctx, "hi")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	start := time.Now()
	for e := range ch {
		if e.Type == EventActionDelta {
			count++
			t.Logf("+%.1fms delta %d: %v", float64(time.Since(start).Microseconds())/1000, count, e.Data)
		}
	}
	if count != 7 {
		t.Fatalf("期望 7 个增量 delta, 实际 %d", count)
	}
}
