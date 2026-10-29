package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

// mockProvider 内嵌 UsageTracker 复用用量统计，
// 与真实 Provider 的构造方式保持一致（新增接口方法时不必逐个改测试替身）。
type mockProvider struct {
	provider.UsageTracker
}

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

// errStreamProvider 模拟流式调用立即失败（网关断流 / ctx 超时被切断）。
type errStreamProvider struct {
	provider.UsageTracker
}

func (m *errStreamProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return nil, nil, errors.New("boom")
}

func (m *errStreamProvider) GenerateStream(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk)
	go func() {
		defer close(ch)
		ch <- schema.StreamChunk{Type: schema.StreamChunkError, Err: errors.New("boom")}
	}()
	return ch, nil
}

// TestStreamRunDeliversErrorEvent 验证客户端正常消费时，
// runLoop 出错必须把 EventError 送达并随后关闭 channel。
func TestStreamRunDeliversErrorEvent(t *testing.T) {
	reg := tools.NewRegistry()
	eng := NewAgentEngine(&errStreamProvider{}, reg, WithGenerateRetries(1))
	ch, err := eng.StreamRun(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	var gotErr bool
	for evt := range ch {
		if evt.Type == EventError {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("runLoop 失败时必须向客户端送达 EventError")
	}
}

// TestStreamRunClosesChannelWhenConsumerStopsReading 复现 TUI 假死场景：
// 客户端事件循环停读（审批弹窗模态、异常中断）时，StreamRun 内部 goroutine
// 不能永久阻塞在 EventError 的发送上——兜底超时后必须放弃发送并关闭 channel，
// 否则 readNextEvent 永远等不到 channel 关闭，输入框拿不回焦点。
func TestStreamRunClosesChannelWhenConsumerStopsReading(t *testing.T) {
	old := errorEventSendTimeout
	errorEventSendTimeout = 100 * time.Millisecond
	defer func() { errorEventSendTimeout = old }()

	reg := tools.NewRegistry()
	eng := NewAgentEngine(&errStreamProvider{}, reg, WithGenerateRetries(1))
	ch, err := eng.StreamRun(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}

	// 全程不消费 channel：超过兜底超时后 channel 必须已关闭。
	time.Sleep(errorEventSendTimeout + 300*time.Millisecond)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("放弃发送后不应再收到事件")
		}
	default:
		t.Fatal("channel 未关闭：错误事件发送永久阻塞（TUI 假死根因）")
	}
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
