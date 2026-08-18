package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
	"go.uber.org/zap"
)

// TestMain 初始化一个静默（仅 Fatal 级别）的全局 logger，
// 避免 engine 内部 log.Info 因未初始化的 nil logger 而 panic。
func TestMain(m *testing.M) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.FatalLevel)
	log.NewLogger(&cfg)
	os.Exit(m.Run())
}

// ---- 测试替身 ----

// fakeProvider 是一个脚本化的 LLMProvider，按调用顺序回放预设响应。
// 既支持 Run 使用的 Generate，也支持 StreamRun 使用的 GenerateStream。
type fakeProvider struct {
	mu sync.Mutex

	genScript       []genStep
	genIdx          int
	genHistories    [][]schema.Message
	genToolsets     [][]schema.ToolDefinition
	genErr          error // 若设置，Generate 直接返回该错误（忽略脚本）

	streamScript []streamStep
	streamIdx    int
	streamHist   [][]schema.Message
}

type genStep struct {
	msg   *schema.Message
	usage *schema.Usage
}

type streamStep struct {
	msg *schema.Message
	// thinking 中的每个字符串作为独立的 StreamChunkThinkingDelta 逐块发出（思考过程）。
	thinking []string
	// text 中的每个字符串作为独立的 StreamChunkTextDelta 逐块发出（正文增量）。
	// 若为空且 msg.Content 非空，则退化为将 msg.Content 作为单块文本发出。
	text []string
	usage *schema.Usage
}

func (p *fakeProvider) Generate(ctx context.Context, history []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.genHistories = append(p.genHistories, copyMessages(history))
	p.genToolsets = append(p.genToolsets, availableTools)

	if p.genErr != nil {
		return nil, nil, p.genErr
	}
	if p.genIdx >= len(p.genScript) {
		return &schema.Message{Role: schema.AssistantRole, Content: ""}, nil, nil
	}
	step := p.genScript[p.genIdx]
	p.genIdx++
	if step.usage == nil {
		step.usage = &schema.Usage{}
	}
	return step.msg, step.usage, nil
}

func (p *fakeProvider) GenerateStream(ctx context.Context, history []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.streamHist = append(p.streamHist, copyMessages(history))

	ch := make(chan schema.StreamChunk)
	if p.streamIdx >= len(p.streamScript) {
		close(ch)
		return ch, nil
	}
	step := p.streamScript[p.streamIdx]
	p.streamIdx++

	usage := step.usage
	if usage == nil {
		usage = &schema.Usage{}
	}

	go func() {
		defer close(ch)
		for _, t := range step.thinking {
			ch <- schema.StreamChunk{Type: schema.StreamChunkThinkingDelta, Delta: t}
		}
		texts := step.text
		if len(texts) == 0 && step.msg != nil && step.msg.Content != "" {
			texts = []string{step.msg.Content}
		}
		for _, t := range texts {
			ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: t}
		}
		ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: step.msg, Usage: usage}
	}()
	return ch, nil
}

func copyMessages(in []schema.Message) []schema.Message {
	out := make([]schema.Message, len(in))
	copy(out, in)
	return out
}

// fakeRegistry 是一个记录调用、可注入行为的工具注册表。
type fakeRegistry struct {
	mu        sync.Mutex
	defs      []schema.ToolDefinition
	executed  []schema.ToolCall
	handler   func(ctx context.Context, call schema.ToolCall) (string, error)
}

func (r *fakeRegistry) Register(tool tools.Tool) error { return nil }

func (r *fakeRegistry) GetAvailableTools() []schema.ToolDefinition {
	return r.defs
}

func (r *fakeRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	r.mu.Lock()
	r.executed = append(r.executed, call)
	r.mu.Unlock()

	if r.handler != nil {
		out, err := r.handler(ctx, call)
		if err != nil {
			return schema.ToolResult{ToolCallID: call.ID, Name: call.Name, Output: err.Error(), IsError: true}
		}
		return schema.ToolResult{ToolCallID: call.ID, Name: call.Name, Output: out, IsError: false}
	}
	return schema.ToolResult{ToolCallID: call.ID, Name: call.Name, Output: "ok", IsError: false}
}

func toolDef(name, desc string) schema.ToolDefinition {
	return schema.ToolDefinition{Name: name, Description: desc}
}

// ---- Run 测试 ----

// TestRun_SingleTurn 验证无工具调用时 Run 正常结束且只发起一次 Generate。
func TestRun_SingleTurn(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "hello world"}, usage: &schema.Usage{InputTokens: 5, OutputTokens: 2}},
		},
	}
	reg := &fakeRegistry{}

	e := engine.NewAgentEngine(p, reg)
	if err := e.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.genIdx != 1 {
		t.Fatalf("期望 Generate 调用 1 次, 实际 %d", p.genIdx)
	}
	if len(reg.executed) != 0 {
		t.Fatalf("不应执行任何工具, 实际 %d", len(reg.executed))
	}
}

// TestRun_ToolThenFinal 验证工具调用 → 结果回灌 → 最终文本 的完整 loop。
func TestRun_ToolThenFinal(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{
				Role: schema.AssistantRole,
				ToolCalls: []schema.ToolCall{
					{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)},
				},
			}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}, usage: &schema.Usage{InputTokens: 10, OutputTokens: 4}},
		},
	}
	reg := &fakeRegistry{
		defs: []schema.ToolDefinition{toolDef("echo", "echo tool")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) {
			if call.Name != "echo" {
				return "", errors.New("unexpected tool")
			}
			return "echoed", nil
		},
	}

	e := engine.NewAgentEngine(p, reg)
	if err := e.Run(context.Background(), "use echo"); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	if len(reg.executed) != 1 {
		t.Fatalf("期望执行 1 次工具, 实际 %d", len(reg.executed))
	}
	if reg.executed[0].Name != "echo" {
		t.Fatalf("期望执行 echo, 实际 %s", reg.executed[0].Name)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.genIdx != 2 {
		t.Fatalf("期望 Generate 调用 2 次, 实际 %d", p.genIdx)
	}
	// 第二轮 Generate 的 history 应已包含工具结果（ToolCallID 关联）。
	if len(p.genHistories) < 2 {
		t.Fatal("缺少第二轮 Generate 的 history")
	}
	lastHist := p.genHistories[1]
	foundResult := false
	for _, m := range lastHist {
		if m.ToolCallID == "c1" && m.Content == "echoed" {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatal("第二轮 history 未包含回灌的工具结果 (ToolCallID=c1, Content=echoed)")
	}
}

// TestRun_ToolError 验证工具失败时 Run 返回错误（触发 Self-Healing 终止）。
func TestRun_ToolError(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{
				Role: schema.AssistantRole,
				ToolCalls: []schema.ToolCall{
					{ID: "c1", Name: "boom", Arguments: json.RawMessage(`{}`)},
				},
			}},
		},
	}
	reg := &fakeRegistry{
		defs:    []schema.ToolDefinition{toolDef("boom", "boom")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) { return "", errors.New("boom failed") },
	}

	e := engine.NewAgentEngine(p, reg)
	if err := e.Run(context.Background(), "boom"); err == nil {
		t.Fatal("工具失败后 Run 应返回错误")
	}
}

// TestRun_GenerateError 验证 Generate 失败时 Run 返回错误。
func TestRun_GenerateError(t *testing.T) {
	p := &fakeProvider{genErr: errors.New("llm down")}
	reg := &fakeRegistry{}

	e := engine.NewAgentEngine(p, reg, engine.WithGenerateRetries(1))
	if err := e.Run(context.Background(), "hi"); err == nil {
		t.Fatal("Generate 失败后 Run 应返回错误")
	}
}

// ---- StreamRun 测试 ----

// collectEvents 从事件 channel 收集所有事件直到关闭，并返回是否出现过 error 事件。
func collectEvents(ch <-chan engine.Event) ([]engine.Event, bool) {
	var events []engine.Event
	gotErr := false
	for evt := range ch {
		if evt.Type == engine.EventError {
			gotErr = true
		}
		events = append(events, evt)
	}
	return events, gotErr
}

// TestStreamRun_ToolThenFinal 验证 StreamRun 在工具场景下正确发出
// tool_start / tool_result / action_delta 并通过关闭 channel 表示完成。
func TestStreamRun_ToolThenFinal(t *testing.T) {
	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{
				Role: schema.AssistantRole,
				ToolCalls: []schema.ToolCall{
					{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)},
				},
			}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done stream"}, usage: &schema.Usage{InputTokens: 9, OutputTokens: 3}},
		},
	}
	reg := &fakeRegistry{
		defs: []schema.ToolDefinition{toolDef("echo", "echo tool")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) {
			return "echoed", nil
		},
	}

	e := engine.NewAgentEngine(p, reg)
	ch, err := e.StreamRun(context.Background(), "use echo")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}

	events, gotErr := collectEvents(ch)
	if gotErr {
		t.Fatal("流式过程不应产生 error 事件")
	}
	if len(reg.executed) != 1 || reg.executed[0].Name != "echo" {
		t.Fatalf("期望执行 1 次 echo 工具, 实际 %d", len(reg.executed))
	}

	var gotToolStart, gotToolResult bool
	var actionText string
	for _, evt := range events {
		switch evt.Type {
		case engine.EventToolStart:
			tc, ok := evt.Data.(schema.ToolCall)
			if !ok || tc.Name != "echo" {
				t.Fatalf("EventToolStart 载荷异常: %#v", evt.Data)
			}
			gotToolStart = true
		case engine.EventToolResult:
			td, ok := evt.Data.(engine.ToolResultData)
			if !ok {
				t.Fatalf("EventToolResult 载荷异常: %#v", evt.Data)
			}
			if td.Result.ToolCallID != "c1" || td.Result.Output != "echoed" {
				t.Fatalf("EventToolResult 内容异常: %#v", td.Result)
			}
			gotToolResult = true
		case engine.EventActionDelta:
			actionText += evt.Data.(string)
		}
	}

	if !gotToolStart {
		t.Error("未收到 EventToolStart")
	}
	if !gotToolResult {
		t.Error("未收到 EventToolResult")
	}
	if actionText != "done stream" {
		t.Fatalf("最终 action delta 期望 'done stream', 实际 %q", actionText)
	}

	// 校验事件顺序：tool_start 必须先于 tool_result，且二者都先于首个 action_delta。
	toolStartIdx, toolResultIdx, firstActionIdx := -1, -1, -1
	for i, evt := range events {
		switch evt.Type {
		case engine.EventToolStart:
			toolStartIdx = i
		case engine.EventToolResult:
			toolResultIdx = i
		case engine.EventActionDelta:
			if firstActionIdx == -1 {
				firstActionIdx = i
			}
		}
	}
	if !(toolStartIdx >= 0 && toolResultIdx > toolStartIdx && firstActionIdx > toolResultIdx) {
		t.Fatalf("事件顺序异常: tool_start=%d tool_result=%d action_delta=%d", toolStartIdx, toolResultIdx, firstActionIdx)
	}
}

// TestStreamRun_SingleTurn 验证无工具场景下 StreamRun 仅发出 action_delta。
func TestStreamRun_SingleTurn(t *testing.T) {
	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "pong"}, usage: &schema.Usage{InputTokens: 3, OutputTokens: 1}},
		},
	}
	reg := &fakeRegistry{}

	e := engine.NewAgentEngine(p, reg)
	ch, err := e.StreamRun(context.Background(), "ping")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}

	events, gotErr := collectEvents(ch)
	if gotErr {
		t.Fatal("流式过程不应产生 error 事件")
	}
	if len(reg.executed) != 0 {
		t.Fatalf("不应执行工具, 实际 %d", len(reg.executed))
	}

	var actionText string
	for _, evt := range events {
		switch evt.Type {
		case engine.EventActionDelta:
			actionText += evt.Data.(string)
		case engine.EventToolStart, engine.EventToolResult:
			t.Fatalf("单轮场景不应出现工具事件: %s", evt.Type)
		}
	}
	if actionText != "pong" {
		t.Fatalf("action delta 期望 'pong', 实际 %q", actionText)
	}
}

// TestStreamRun_ThinkingDelta 验证思考过程以多个 thinking_delta 块流式输出，
// 且正文与思考分别通过 action_delta / thinking_delta 事件独立透传。
func TestStreamRun_ThinkingDelta(t *testing.T) {
	// 模型先逐字输出思考 “你”“好”“啊”，再以单块文本给出最终答复。
	p := &fakeProvider{
		streamScript: []streamStep{
			{
				thinking: []string{"你", "好", "啊"},
				text:     []string{"hi there"},
				msg:      &schema.Message{Role: schema.AssistantRole, Content: "hi there"},
				usage:    &schema.Usage{InputTokens: 4, OutputTokens: 6},
			},
		},
	}
	reg := &fakeRegistry{}

	e := engine.NewAgentEngine(p, reg)
	ch, err := e.StreamRun(context.Background(), "在吗")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}

	events, gotErr := collectEvents(ch)
	if gotErr {
		t.Fatal("流式过程不应产生 error 事件")
	}
	if len(reg.executed) != 0 {
		t.Fatalf("不应执行工具, 实际 %d", len(reg.executed))
	}

	var thinkingText, actionText string
	var thinkingCount int
	for _, evt := range events {
		switch evt.Type {
		case engine.EventThinkingDelta:
			thinkingText += evt.Data.(string)
			thinkingCount++
		case engine.EventActionDelta:
			actionText += evt.Data.(string)
		case engine.EventToolStart, engine.EventToolResult:
			t.Fatalf("无工具场景不应出现工具事件: %s", evt.Type)
		}
	}

	if thinkingCount != 3 {
		t.Fatalf("期望 3 个 thinking_delta 事件, 实际 %d", thinkingCount)
	}
	if thinkingText != "你好啊" {
		t.Fatalf("思考过程拼接期望 '你好啊', 实际 %q", thinkingText)
	}
	if actionText != "hi there" {
		t.Fatalf("正文 action delta 期望 'hi there', 实际 %q", actionText)
	}
}

// TestStreamRun_Error 验证 GenerateStream 失败时 StreamRun 发出 error 事件。
func TestStreamRun_Error(t *testing.T) {
	// 通过在第一帧发出 error chunk 来模拟失败。
	pErr := &errorStreamProvider{err: errors.New("stream broke")}
	reg := &fakeRegistry{}

	e := engine.NewAgentEngine(pErr, reg, engine.WithGenerateRetries(1))
	ch, err := e.StreamRun(context.Background(), "hi")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}
	_, gotErr := collectEvents(ch)
	if !gotErr {
		t.Fatal("流式失败应产生 error 事件")
	}
}

// errorStreamProvider 仅在 GenerateStream 第一帧返回 error chunk。
type errorStreamProvider struct {
	err error
}

func (p *errorStreamProvider) Generate(ctx context.Context, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return &schema.Message{Role: schema.AssistantRole}, nil, nil
}

func (p *errorStreamProvider) GenerateStream(ctx context.Context, history []schema.Message, tools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk, 1)
	ch <- schema.StreamChunk{Type: schema.StreamChunkError, Err: p.err}
	close(ch)
	return ch, nil
}

var _ provider.LLMProvider = (*fakeProvider)(nil)
var _ provider.LLMProvider = (*errorStreamProvider)(nil)
var _ tools.Registry = (*fakeRegistry)(nil)
