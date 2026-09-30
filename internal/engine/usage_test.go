package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

// TestUsageReportedOncePerLLMCall 验证用量采集已收敛到"每次 LLM 调用一次"：
// 工具执行不消耗 token，不应再产生额外的用量事件；上报值应为会话累计值。
func TestUsageReportedOncePerLLMCall(t *testing.T) {
	call := schema.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}
	p := &fakeProvider{
		streamScript: []streamStep{
			// 第 1 轮：发起一次工具调用
			{
				msg:   &schema.Message{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{call}},
				usage: &schema.Usage{InputTokens: 10, OutputTokens: 5},
			},
			// 第 2 轮：给出最终文本，循环结束
			{
				msg:   &schema.Message{Role: schema.AssistantRole, Content: "done"},
				usage: &schema.Usage{InputTokens: 20, OutputTokens: 7},
			},
		},
	}
	reg := &fakeRegistry{
		defs:    []schema.ToolDefinition{toolDef("echo", "echo tool")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) { return "echoed", nil },
	}

	e := engine.NewAgentEngine(p, reg)
	ch, err := e.StreamRun(context.Background(), "count tokens")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}

	var usages []schema.Usage
	for evt := range ch {
		// 契约：EventUsage 的载荷固定为 *schema.Usage
		if evt.Type == engine.EventUsage {
			if u, ok := evt.Data.(*schema.Usage); ok && u != nil {
				usages = append(usages, *u)
			} else {
				t.Errorf("EventUsage 载荷应为 *schema.Usage, 实际 %T", evt.Data)
			}
		}
	}

	// 两次 LLM 调用 → 两条用量事件（工具阶段不再重复上报）
	if len(usages) != 2 {
		t.Fatalf("期望 2 条用量事件（每次 LLM 调用一条）, 实际 %d: %+v", len(usages), usages)
	}
	if usages[0].InputTokens != 10 || usages[0].OutputTokens != 5 {
		t.Fatalf("第 1 条应为本次调用用量 {10,5}, 实际 %+v", usages[0])
	}
	if usages[1].InputTokens != 30 || usages[1].OutputTokens != 12 {
		t.Fatalf("第 2 条应为会话累计用量 {30,12}, 实际 %+v", usages[1])
	}
	if got := e.Usage(); got.InputTokens != 30 || got.OutputTokens != 12 {
		t.Fatalf("引擎累计用量应为 {30,12}, 实际 %+v", got)
	}
}

// TestUsageSurvivesToolCall 验证工具调用不会污染用量累计（工具本身不消耗 token）。
func TestUsageSurvivesToolCall(t *testing.T) {
	call := schema.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}
	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{call}}, usage: &schema.Usage{InputTokens: 3, OutputTokens: 1}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}}, // 无 usage：不应报错
		},
	}
	reg := &fakeRegistry{
		defs:    []schema.ToolDefinition{toolDef("echo", "echo tool")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) { return "echoed", nil },
	}

	e := engine.NewAgentEngine(p, reg)
	ch, err := e.StreamRun(context.Background(), "x")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}
	for range ch {
	}
	if got := e.Usage(); got.InputTokens != 3 || got.OutputTokens != 1 {
		t.Fatalf("nil 用量应被忽略且不影响累计, 期望 {3,1}, 实际 %+v", got)
	}
}

var _ tools.Registry = (*fakeRegistry)(nil)
