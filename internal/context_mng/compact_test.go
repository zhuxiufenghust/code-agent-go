package context_mng

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// ---------- EstimateTokens ----------

func TestEstimateTokens_Empty(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Errorf("空消息列表应为 0 tokens, 实际 %d", got)
	}
}

func TestEstimateTokens_ContentOnly(t *testing.T) {
	// charsPerToken = 4，每条消息另有 tokensPerMessage 的固定开销
	msgs := []schema.Message{{Role: schema.UserRole, Content: "12345678"}}
	want := 8/charsPerToken + tokensPerMessage
	if got := EstimateTokens(msgs); got != want {
		t.Errorf("8 字符 + 1 条消息应为 %d tokens, 实际 %d", want, got)
	}
}

func TestEstimateTokens_CountsToolCallFields(t *testing.T) {
	msgs := []schema.Message{
		{
			Role:    schema.AssistantRole,
			Content: "",
			ToolCalls: []schema.ToolCall{
				{ID: "call_1", Name: "read_tool", Arguments: []byte(`{"path":"a.go"}`)},
			},
		},
	}
	want := (len("call_1")+len("read_tool")+len(`{"path":"a.go"}`))/charsPerToken + tokensPerMessage
	if got := EstimateTokens(msgs); got != want {
		t.Errorf("tool_call 字段应计入: 期望 %d, 实际 %d", want, got)
	}
}

func TestEstimateTokens_CountsToolCallID(t *testing.T) {
	msgs := []schema.Message{
		{Role: schema.UserRole, ToolCallID: "call_12345678", Content: "ok"},
	}
	want := (len("call_12345678")+len("ok"))/charsPerToken + tokensPerMessage
	if got := EstimateTokens(msgs); got != want {
		t.Errorf("ToolCallID 应计入: 期望 %d, 实际 %d", want, got)
	}
}

// TestEstimateTokens_CountsRunesNotBytes 验证中文按 rune 计数（不再按 UTF-8 字节低估）。
func TestEstimateTokens_CountsRunesNotBytes(t *testing.T) {
	cn := EstimateTokens([]schema.Message{{Role: schema.UserRole, Content: strings.Repeat("中", 400)}})
	en := EstimateTokens([]schema.Message{{Role: schema.UserRole, Content: strings.Repeat("a", 400)}})
	if cn != en {
		t.Errorf("相同字符数的中英文应估算出相同 token: 中文 %d, 英文 %d", cn, en)
	}
}

// ---------- repairOrphanedToolPairs ----------

func TestRepairOrphanedToolPairs_CompletePairUntouched(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read_tool"}}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "file content"},
	}
	got := repairOrphanedToolPairs(in)
	if len(got) != len(in) {
		t.Fatalf("完整工具对不应被增删: 期望 %d 条, 实际 %d 条", len(in), len(got))
	}
	if got[2].ToolCallID != "c1" || got[2].Content != "file content" {
		t.Errorf("tool_result 应保持原样, 实际 %+v", got[2])
	}
}

func TestRepairOrphanedToolPairs_DropsOrphanToolResult(t *testing.T) {
	// assistant 消息被压缩掉后，只留下孤立的 tool_result
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, ToolCallID: "c_gone", Content: "stale output"},
		{Role: schema.UserRole, Content: "hello"},
	}
	got := repairOrphanedToolPairs(in)
	if len(got) != 2 {
		t.Fatalf("孤立 tool_result 应被删除: 期望 2 条, 实际 %d 条", len(got))
	}
	for _, m := range got {
		if m.ToolCallID == "c_gone" {
			t.Errorf("孤立 tool_result 未被删除: %+v", m)
		}
	}
}

func TestRepairOrphanedToolPairs_InsertsPlaceholderForMissingResult(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "bash_tool"}}},
	}
	got := repairOrphanedToolPairs(in)
	if len(got) != 3 {
		t.Fatalf("缺少响应的 tool_call 应补占位 tool_result: 期望 3 条, 实际 %d 条", len(got))
	}
	p := got[2]
	if p.Role != schema.UserRole || p.ToolCallID != "c1" || p.Content == "" {
		t.Errorf("占位消息格式错误: %+v", p)
	}
}

func TestRepairOrphanedToolPairs_PartialResults(t *testing.T) {
	in := []schema.Message{
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "read_tool"},
			{ID: "c2", Name: "bash_tool"},
		}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "out1"},
	}
	got := repairOrphanedToolPairs(in)
	// assistant + c1 结果 + c2 占位；占位排在已有结果之后，不打乱原顺序
	if len(got) != 3 {
		t.Fatalf("期望 3 条 (assistant + 1 result + 1 占位), 实际 %d 条", len(got))
	}
	if got[1].ToolCallID != "c1" || got[1].Content != "out1" {
		t.Errorf("已有结果应保持原位, 实际 %+v", got[1])
	}
	if got[2].ToolCallID != "c2" || got[2].Content != placeholderToolResult {
		t.Errorf("占位消息应排在已有结果之后, 实际 %+v", got[2])
	}
}

func TestRepairOrphanedToolPairs_EmptyInput(t *testing.T) {
	if got := repairOrphanedToolPairs(nil); len(got) != 0 {
		t.Errorf("空输入应返回空切片, 实际 %d 条", len(got))
	}
}

func TestRepairOrphanedToolPairs_DropsDuplicateResult(t *testing.T) {
	in := []schema.Message{
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read_tool"}}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "first"},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "second"},
	}
	got := repairOrphanedToolPairs(in)
	if len(got) != 2 {
		t.Fatalf("同一 tool_call 的重复结果只应保留第一条: 期望 2 条, 实际 %d 条", len(got))
	}
	if got[1].Content != "first" {
		t.Errorf("应保留第一条结果, 实际 %+v", got[1])
	}
}

func TestRepairOrphanedToolPairs_PlaceholderPrecedesNextUserMessage(t *testing.T) {
	// 占位消息必须紧跟在该 assistant 的 tool_result 之后、下一条普通消息之前。
	in := []schema.Message{
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "read_tool"},
			{ID: "c2", Name: "bash_tool"},
		}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "out1"},
		{Role: schema.UserRole, Content: "next question"},
	}
	got := repairOrphanedToolPairs(in)
	want := []struct {
		toolCallID string
		content    string
	}{
		{"", ""},                      // assistant
		{"c1", "out1"},                // 已有结果
		{"c2", placeholderToolResult}, // 占位
		{"", "next question"},         // 后续普通消息
	}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 条, 实际 %d 条: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i].ToolCallID != w.toolCallID || got[i].Content != w.content {
			t.Errorf("第 %d 条不符: 期望 {%q %q}, 实际 {%q %q}",
				i, w.toolCallID, w.content, got[i].ToolCallID, got[i].Content)
		}
	}
}

// ---------- SlidingWindowCompactor ----------

func TestNewSlidingWindowCompactor_Defaults(t *testing.T) {
	c := NewSlidingWindowCompactor(0)
	if c.MaxMessages != 100 {
		t.Errorf("maxMessages<=0 时应回落为 100, 实际 %d", c.MaxMessages)
	}
	c = NewSlidingWindowCompactor(-5)
	if c.MaxMessages != 100 {
		t.Errorf("负数应回落为 100, 实际 %d", c.MaxMessages)
	}
}

func TestSlidingWindowCompactor_ZeroValueAndTinyLimit(t *testing.T) {
	if got := (&SlidingWindowCompactor{}).maxMessages(); got != 100 {
		t.Errorf("MaxMessages<=0 时应回落 100, 实际 %d", got)
	}
	// 过小的窗口必须至少容纳 system + 一条对话
	if got := (&SlidingWindowCompactor{MaxMessages: 1}).maxMessages(); got != 2 {
		t.Errorf("MaxMessages<2 时应回落 2, 实际 %d", got)
	}
	c := NewSlidingWindowCompactor(1)
	got, err := c.Compact(context.Background(), []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "a"},
		{Role: schema.AssistantRole, Content: "b"},
	})
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 2 || got[1].Content != "b" {
		t.Errorf("窗口过小时应保留 system + 最近一条, 实际 %+v", got)
	}
}

func TestSlidingWindowCompactor_EmptyAndNoSystemHead(t *testing.T) {
	c := NewSlidingWindowCompactor(3)
	if got, err := c.Compact(context.Background(), nil); err != nil || len(got) != 0 {
		t.Errorf("空输入应原样返回: got=%v err=%v", got, err)
	}
	// 首条不是 system 时同样按窗口压缩（system 段为空）
	in := []schema.Message{
		{Role: schema.UserRole, Content: "1"},
		{Role: schema.AssistantRole, Content: "2"},
		{Role: schema.UserRole, Content: "3"},
		{Role: schema.AssistantRole, Content: "4"},
	}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("无 system 段时应保留最近 3 条, 实际 %d 条", len(got))
	}
	if got[0].Content != "2" || got[2].Content != "4" {
		t.Errorf("应保留最近 3 条: %+v", got)
	}
}

func TestSlidingWindowCompactor_UnderLimitUntouched(t *testing.T) {
	c := NewSlidingWindowCompactor(10)
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "hi"},
	}
	got, _ := c.Compact(context.Background(), in)
	if len(got) != len(in) {
		t.Errorf("未超上限应原样返回: 期望 %d 条, 实际 %d 条", len(in), len(got))
	}
}

func TestSlidingWindowCompactor_KeepsSystemAndTail(t *testing.T) {
	in := []schema.Message{{Role: schema.SystemRole, Content: "sys"}}
	for i := 0; i < 10; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("m", i+1)})
	}
	c := NewSlidingWindowCompactor(5)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	// MaxMessages 含 system 本身：5 条 = 1 system + 4 尾部
	if len(got) != 5 {
		t.Fatalf("期望保留 5 条, 实际 %d 条", len(got))
	}
	if got[0].Role != schema.SystemRole || got[0].Content != "sys" {
		t.Errorf("system 必须固定在首位, 实际 %+v", got[0])
	}
	tail := in[len(in)-(len(got)-1):]
	for i, want := range tail {
		if got[i+1].Content != want.Content {
			t.Errorf("第 %d 条尾部消息不匹配: 期望 %q, 实际 %q", i, want.Content, got[i+1].Content)
		}
	}
}

func TestSlidingWindowCompactor_DoesNotMutateInput(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "a"},
		{Role: schema.AssistantRole, Content: "b"},
	}
	c := NewSlidingWindowCompactor(2)
	if _, err := c.Compact(context.Background(), in); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(in) != 3 || in[0].Content != "sys" || in[1].Content != "a" {
		t.Errorf("Compact 不应修改入参切片: %+v", in)
	}
}

func TestSlidingWindowCompactor_RepairsOrphanToolResult(t *testing.T) {
	// 截头后 assistant 被丢弃，其 tool_result 成为孤立消息，应被删除。
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read_tool"}}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "old output"},
		{Role: schema.UserRole, Content: "new question"},
	}
	c := NewSlidingWindowCompactor(3)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("孤立 tool_result 应被删除: 期望 2 条 (system+最新一条), 实际 %d 条", len(got))
	}
	for _, m := range got {
		if m.ToolCallID == "c1" {
			t.Errorf("孤立 tool_result 未被删除: %+v", m)
		}
	}
}

func TestSlidingWindowCompactor_CanceledContext(t *testing.T) {
	c := NewSlidingWindowCompactor(2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Compact(ctx, []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "a"},
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx 已取消时应返回 context.Canceled, 实际 %v", err)
	}
}

// ---------- TokenBudgetCompactor ----------

func TestNewTokenBudgetCompactor_Defaults(t *testing.T) {
	tbc := NewTokenBudgetCompactor(200_000)
	if tbc.MaxTokens != 160_000 {
		t.Errorf("200K 窗口的 80%% 应为 160000, 实际 %d", tbc.MaxTokens)
	}
	if tbc.MinTailMessages != 6 {
		t.Errorf("默认 MinTailMessages 应为 6, 实际 %d", tbc.MinTailMessages)
	}
}

func TestTokenBudgetCompactor_ZeroValueDefaults(t *testing.T) {
	c := &TokenBudgetCompactor{}
	if got := c.maxTokens(); got != 160_000 {
		t.Errorf("MaxTokens<=0 时应回落 160000, 实际 %d", got)
	}
	if got := c.minTail(); got != 6 {
		t.Errorf("MinTailMessages<=0 时应回落 6, 实际 %d", got)
	}
}

func TestTokenBudgetCompactor_UnderBudgetUntouched(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "hi"},
	}
	c := NewTokenBudgetCompactor(200_000)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != len(in) {
		t.Errorf("未超预算应原样返回: 期望 %d 条, 实际 %d 条", len(in), len(got))
	}
}

func TestTokenBudgetCompactor_NoSystemHeadStillCompacts(t *testing.T) {
	// 无 system 段时同样参与预算压缩
	in := []schema.Message{
		{Role: schema.UserRole, Content: strings.Repeat("x", 400)},
		{Role: schema.AssistantRole, Content: strings.Repeat("y", 400)},
		{Role: schema.UserRole, Content: strings.Repeat("z", 400)},
	}
	c := &TokenBudgetCompactor{MaxTokens: 1, MinTailMessages: 1}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("无 system 段时应压缩到 minTail 条, 实际 %d 条", len(got))
	}
	if got[0].Content != strings.Repeat("z", 400) {
		t.Errorf("应保留最近一条, 实际 %q", got[0].Content)
	}
}

func TestTokenBudgetCompactor_TailShorterThanMinTailUntouched(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: strings.Repeat("x", 400)},
	}
	c := &TokenBudgetCompactor{MaxTokens: 1, MinTailMessages: 5}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("非 system 消息数 <= minTail 时应原样返回, 实际 %d 条", len(got))
	}
}

func TestTokenBudgetCompactor_OverBudgetTruncatesOldest(t *testing.T) {
	in := []schema.Message{{Role: schema.SystemRole, Content: "sys"}}
	for i := 0; i < 12; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("y", 400)})
	}
	c := &TokenBudgetCompactor{MaxTokens: 500, MinTailMessages: 2}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) >= len(in) {
		t.Fatalf("超预算时应丢弃最旧的消息: 原始 %d 条, 实际 %d 条", len(in), len(got))
	}
	if gotEstimate := EstimateTokens(got); gotEstimate > 500 {
		t.Errorf("压缩结果应落入预算: 期望 <= 500 tokens, 实际 %d", gotEstimate)
	}
	if got[0].Role != schema.SystemRole || got[0].Content != "sys" {
		t.Errorf("system 应在首位, 实际 %+v", got[0])
	}
	// 尾部 minTail 条必须保留
	for i, want := range in[len(in)-2:] {
		if got[len(got)-2+i].Content != want.Content {
			t.Errorf("尾部第 %d 条不匹配: 期望 %q, 实际 %q", i, want.Content, got[len(got)-2+i].Content)
		}
	}
}

func TestTokenBudgetCompactor_OverBudgetKeepsMinTailAsFloor(t *testing.T) {
	// 即使 system+tail 仍超预算，也至少保留 minTail 条最近对话
	in := []schema.Message{{Role: schema.SystemRole, Content: strings.Repeat("s", 4000)}}
	for i := 0; i < 5; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("y", 4000)})
	}
	c := &TokenBudgetCompactor{MaxTokens: 1, MinTailMessages: 2}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(got) != 3 { // system + 2
		t.Fatalf("期望退化为 system + minTail 条, 实际 %d 条", len(got))
	}
}

func TestTokenBudgetCompactor_GreedyKeepsAsMuchAsFits(t *testing.T) {
	// 每条 100 字符 ≈ 25+4 tokens；预算应尽可能多地保留历史
	in := []schema.Message{{Role: schema.SystemRole, Content: "sys"}}
	for i := 0; i < 10; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("y", 100)})
	}
	c := &TokenBudgetCompactor{MaxTokens: 150, MinTailMessages: 2}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if EstimateTokens(got) > 150 {
		t.Fatalf("结果超预算: %d tokens", EstimateTokens(got))
	}
	// 再多保留一条就会超预算
	if len(got)+1 <= len(in) {
		more := joinMessages([]schema.Message{got[0]}, in[len(in)-(len(got)-1)-1:])
		if EstimateTokens(more) <= 150 {
			t.Errorf("未尽可能多地保留历史: 保留 %d 条, 但 %d 条仍在预算内", len(got), len(got)+1)
		}
	}
}

func TestTokenBudgetCompactor_OrphanToolResultRepaired(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{{ID: "c1", Name: "read_tool"}}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: strings.Repeat("o", 400)},
	}
	for i := 0; i < 8; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("y", 400)})
	}
	c := &TokenBudgetCompactor{MaxTokens: 200, MinTailMessages: 2}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	for _, m := range got {
		if m.ToolCallID != "" && m.Role == schema.AssistantRole {
			t.Errorf("assistant 消息不应带 ToolCallID: %+v", m)
		}
	}
	// 工具对必须完整：要么 assistant+result 都在，要么都不在
	var hasCall, hasResult bool
	for _, m := range got {
		if m.Role == schema.AssistantRole && len(m.ToolCalls) > 0 {
			hasCall = true
		}
		if m.ToolCallID == "c1" {
			hasResult = true
		}
	}
	if hasResult && !hasCall {
		t.Errorf("存在孤立 tool_result: %+v", got)
	}
	if hasCall && !hasResult {
		t.Errorf("存在缺响应的 tool_call: %+v", got)
	}
}

func TestTokenBudgetCompactor_CanceledContext(t *testing.T) {
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "hi"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewTokenBudgetCompactor(200_000)
	if _, err := c.Compact(ctx, in); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx 已取消时应返回 context.Canceled, 实际 %v", err)
	}
}
