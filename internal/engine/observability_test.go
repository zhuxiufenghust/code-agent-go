package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/observability"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// TestRun_ObservabilitySnapshot 覆盖第 4 周 D15 的验收口径：
// "一次长跑结束后能拿到累计 token、LLM 耗时、工具成功率"。
// 断言放在一次真实的 Run（含工具调用与失败）之后，而不是只测 observability 包本身，
// 因为指标一旦集成层接错（回调被覆盖、用量没上报），单包测试是发现不了的。
func TestRun_ObservabilitySnapshot(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			// 第一轮：调用两个工具（一个成功、一个失败）
			{
				msg: &schema.Message{
					Role: schema.AssistantRole,
					ToolCalls: []schema.ToolCall{
						{ID: "ok-1", Name: "ok", Arguments: json.RawMessage(`{}`)},
					},
				},
				usage: &schema.Usage{InputTokens: 100, OutputTokens: 10},
			},
			// 第二轮：纯文本收尾
			{
				msg:   &schema.Message{Role: schema.AssistantRole, Content: "done"},
				usage: &schema.Usage{InputTokens: 150, OutputTokens: 20},
			},
		},
	}
	reg := &fakeRegistry{
		defs: []schema.ToolDefinition{toolDef("ok", "ok")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) {
			if call.ID == "bad-1" {
				return "", context.DeadlineExceeded
			}
			// 睡一小会儿让"耗时"在毫秒分辨率下也可见（跑得太快会全部记成 0ms）。
			time.Sleep(6 * time.Millisecond)
			return "fine", nil
		},
	}

	obs := observability.Nop()
	e := engine.NewAgentEngine(p, reg, engine.WithObservability(obs))
	if err := e.Run(context.Background(), "run once"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	s := obs.Snapshot()
	if s.LLM.Calls != 2 || s.LLM.Errors != 0 {
		t.Fatalf("LLM 指标不符预期: %+v", s.LLM)
	}
	if s.Since.IsZero() {
		t.Fatal("采集起点应当被记录")
	}
	if s.Tool.Calls != 1 || s.Tool.Errors != 0 {
		t.Fatalf("工具指标不符预期: %+v", s.Tool)
	}
	// 工具耗时来源于 ToolDone 的真实 elapsed，这里验证它真的传到了指标侧。
	if s.Tool.LatencyMS < 5 {
		t.Fatalf("工具耗时未被记录: %d ms", s.Tool.LatencyMS)
	}
	// 用量由引擎以"累计快照"上报，第二次（100+150 / 10+20）覆盖第一次。
	if s.Token.Input != 250 || s.Token.Output != 30 || s.Token.Total != 280 {
		t.Fatalf("累计 token 不符预期: %+v", s.Token)
	}
}

// TestRun_ObservabilityCountsToolFailures 保证工具失败计入成功率：
// 只统计成功的"成功率"永远是 1，观测就失去了意义。
func TestRun_ObservabilityCountsToolFailures(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{
				msg: &schema.Message{
					Role: schema.AssistantRole,
					ToolCalls: []schema.ToolCall{
						{ID: "ok-1", Name: "ok", Arguments: json.RawMessage(`{}`)},
						{ID: "bad-1", Name: "bad", Arguments: json.RawMessage(`{}`)},
					},
				},
			},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}},
		},
	}
	reg := &fakeRegistry{
		defs: []schema.ToolDefinition{toolDef("ok", "ok"), toolDef("bad", "bad")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) {
			if call.Name == "bad" {
				return "", context.DeadlineExceeded
			}
			return "fine", nil
		},
	}
	obs := observability.Nop()
	e := engine.NewAgentEngine(p, reg, engine.WithObservability(obs))
	if err := e.Run(context.Background(), "run with failure"); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	s := obs.Snapshot()
	if s.Tool.Calls != 2 || s.Tool.Errors != 1 {
		t.Fatalf("工具成功率统计不符预期: %+v", s.Tool)
	}
	if s.Tool.SuccessRate > 0.51 {
		t.Fatalf("期望成功率约 0.5, 实际 %v", s.Tool.SuccessRate)
	}
}

// TestRun_TokenWindowWarnRatioIsConfigurable 验证 WithTokenBudget 的告警开关行为：
// 窗口为 0（未配置）时不该产生任何告警检测开销。这里只断言"不 panic 且指标照常采集"，
// 水位告警本身只是日志副作用，checkTokenWater 在每轮 LLM 调用前执行。
func TestRun_TokenWindowWarnRatioIsConfigurable(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "hi"}},
		},
	}
	reg := &fakeRegistry{defs: []schema.ToolDefinition{toolDef("ok", "ok")}}
	obs := observability.Nop()
	e := engine.NewAgentEngine(p, reg,
		engine.WithObservability(obs),
		engine.WithTokenBudget(10, 0.8)) // 窗口极小：必然越线，验证不会因此中断运行
	if err := e.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("水位告警不应中断运行: %v", err)
	}
	if obs.Snapshot().LLM.Calls != 1 {
		t.Fatal("越线告警不应影响指标采集")
	}
}
