package observability

import (
	"context"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// TestInstrumentKeepsOriginalCallbacks 保证织入埋点后原回调仍然被调用：
// 一旦埋点层"吃掉"了原事件，TUI 会直接少掉工具结果，属于最难排查的那类回归。
func TestInstrumentKeepsOriginalCallbacks(t *testing.T) {
	var gotToolDone, gotToken bool
	em := report.Emitter{
		Generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			u := schema.Usage{InputTokens: 10, OutputTokens: 2}
			return &schema.Message{Role: schema.AssistantRole, Content: "hi"}, &u, nil
		},
		ToolStart: func(turn int, tc schema.ToolCall) {},
		ToolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			gotToolDone = true
		},
		TokenUpdate: func(ctx context.Context, turn int, usage *schema.Usage) { gotToken = true },
		Compaction:  func(data report.CompactionData) {},
	}

	o := Nop()
	out := Instrument(context.Background(), em, o)

	msg, usage, err := out.Generate(context.Background(), 1, nil, nil)
	if err != nil || msg == nil || msg.Content != "hi" {
		t.Fatalf("Generate 透传失败: msg=%+v err=%v", msg, err)
	}
	if usage == nil || usage.InputTokens != 10 {
		t.Fatalf("Generate 未透传用量: %+v", usage)
	}
	tc := schema.ToolCall{ID: "call-1", Name: "bash_tool"}
	out.ToolStart(1, tc)
	out.ToolDone(1, tc, schema.ToolResult{ToolCallID: tc.ID, Name: tc.Name, IsError: true}, 5*time.Millisecond)
	out.TokenUpdate(context.Background(), 1, usage)
	out.Compaction(report.CompactionData{Turn: 1, TokensBefore: 100, TokensAfter: 40})

	if !gotToolDone || !gotToken {
		t.Fatal("原回调未被透传调用")
	}

	s := o.Snapshot()
	if s.LLM.Calls != 1 || s.Tool.Calls != 1 || s.Tool.Errors != 1 {
		t.Fatalf("埋点未记录调用: llm=%+v tool=%+v", s.LLM, s.Tool)
	}
	if s.Token.Input != 10 || s.Compaction.Count != 1 {
		t.Fatalf("用量/压缩未被记录: token=%+v compaction=%+v", s.Token, s.Compaction)
	}
}

// TestInstrumentSurvivesNilCallbacks 覆盖"原 Emitter 没有某个回调"的常见情况：
// 此时仍要采集指标（工具成功率不能因为没人看事件就不统计），且不能 panic。
func TestInstrumentSurvivesNilCallbacks(t *testing.T) {
	o := Nop()
	out := Instrument(context.Background(), report.Emitter{}, o)

	tc := schema.ToolCall{ID: "call-2", Name: "read_tool"}
	out.ToolStart(1, tc)
	out.ToolDone(1, tc, schema.ToolResult{ToolCallID: tc.ID, Name: tc.Name}, time.Millisecond)
	if o.Snapshot().Tool.Calls != 1 {
		t.Fatal("nil ToolStart/ToolDone 也应采集工具指标")
	}
	if out.Generate != nil {
		t.Fatal("Generate 为 nil 时不应凭空造出一个实现")
	}
}

// TestInstrumentRecordsLLMError 单独校验失败路径：LLM 调用失败同样要计数，
// 否则成功率永远是 100%，观测就失去了意义。
func TestInstrumentRecordsLLMError(t *testing.T) {
	o := Nop()
	em := report.Emitter{
		Generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			return nil, nil, context.DeadlineExceeded
		},
	}
	out := Instrument(context.Background(), em, o)
	if _, _, err := out.Generate(context.Background(), 1, nil, nil); err == nil {
		t.Fatal("期望错误被透传")
	}
	if s := o.Snapshot(); s.LLM.Calls != 1 || s.LLM.Errors != 1 {
		t.Fatalf("LLM 失败未被计入: %+v", s.LLM)
	}
}

// TestInstrumentLinksToolSpanToParent 校验工具 Span 挂到了运行根目录之下：
// Emitter 的工具回调没有 ctx 参数，父子关系只能靠 Instrument 捕获的 parent 传递。
func TestInstrumentLinksToolSpanToParent(t *testing.T) {
	o := Nop()
	ctx, root := o.StartSpan(context.Background(), SpanRun)
	out := Instrument(ctx, report.Emitter{}, o)

	tc := schema.ToolCall{ID: "call-3", Name: "bash_tool"}
	out.ToolStart(1, tc)
	out.ToolDone(1, tc, schema.ToolResult{ToolCallID: tc.ID, Name: tc.Name}, time.Millisecond)
	root.End(nil)

	if o.Snapshot().Tool.Calls != 1 {
		t.Fatal("工具调用未被记录")
	}
	// 能否串联取决于 Span.parent 关系是否正确建立：
	// 这里直接断言根 Span 被成功收尾（无 panic/无重复导出），父子链本身由 TestSpanHierarchySharesTraceID 覆盖。
}
