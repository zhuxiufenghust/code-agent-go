package context_mng

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// stubSummarizer 记录被调用时的入参并返回预设结果。
type stubSummarizer struct {
	calls      int
	gotMsgs    []schema.Message
	gotCtxDone bool
	content    string
	err        error
	nilResp    bool
}

func (s *stubSummarizer) Generate(ctx context.Context, msgs []schema.Message, _ []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	s.calls++
	s.gotMsgs = msgs
	s.gotCtxDone = ctx.Err() != nil
	if s.err != nil {
		return nil, nil, s.err
	}
	if s.nilResp {
		return nil, nil, nil
	}
	content := s.content
	if content == "" {
		content = "**Goal:** 摘要内容"
	}
	return &schema.Message{Role: schema.AssistantRole, Content: content}, &schema.Usage{}, nil
}

// stubCompactor 记录是否被调用，用于验证 fallback 链路。
type stubCompactor struct {
	calls int
	in    []schema.Message
	out   []schema.Message
	err   error
}

func (c *stubCompactor) Compact(_ context.Context, msgs []schema.Message) ([]schema.Message, error) {
	c.calls++
	c.in = msgs
	if c.err != nil {
		return nil, c.err
	}
	if c.out != nil {
		return c.out, nil
	}
	return msgs, nil
}

// longConversation 构造 system + n 条对话，确保 EstimateTokens 远超 maxTokens。
func longConversation(n int) []schema.Message {
	msgs := []schema.Message{{Role: schema.SystemRole, Content: "you are a code agent"}}
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			msgs = append(msgs, schema.Message{Role: schema.UserRole, Content: strings.Repeat("u", 400)})
		} else {
			msgs = append(msgs, schema.Message{Role: schema.AssistantRole, Content: strings.Repeat("a", 400)})
		}
	}
	return msgs
}

// ---------- 构造 ----------

func TestNewSummarizationCompactor_Defaults(t *testing.T) {
	c := NewSummarizationCompactor(&stubSummarizer{}, 200_000, 0, nil)
	if c.MaxTokens != 160_000 {
		t.Errorf("200K 窗口的 80%% 应为 160000, 实际 %d", c.MaxTokens)
	}
	if c.MinTailMessages != 6 {
		t.Errorf("minTailMessages<=0 应回落为 6, 实际 %d", c.MinTailMessages)
	}
	if c.ContextWindow != 200_000 {
		t.Errorf("ContextWindow 应被保留, 实际 %d", c.ContextWindow)
	}
}

func TestSummarizationCompactor_ZeroValueDefaults(t *testing.T) {
	c := &SummarizationCompactor{}
	if got := c.maxTokens(); got != 160_000 {
		t.Errorf("MaxTokens<=0 应回落 160000, 实际 %d", got)
	}
	if got := c.minTail(); got != 6 {
		t.Errorf("MinTailMessages<=0 应回落 6, 实际 %d", got)
	}
	if _, ok := c.fallback().(*TokenBudgetCompactor); !ok {
		t.Errorf("Fallback 为 nil 时应回落为 *TokenBudgetCompactor, 实际 %T", c.fallback())
	}
	if got := c.summaryTimeout(); got != defaultSummaryTimeout {
		t.Errorf("SummaryTimeout<=0 时应回落为 %v, 实际 %v", defaultSummaryTimeout, got)
	}
}

func TestSummarizationCompactor_CustomSummaryTimeout(t *testing.T) {
	c := NewSummarizationCompactor(&stubSummarizer{}, 200_000, 6, nil)
	if c.SummaryTimeout != defaultSummaryTimeout {
		t.Errorf("构造时应设置默认摘要超时, 实际 %v", c.SummaryTimeout)
	}
	c.SummaryTimeout = 5 * time.Millisecond
	c.MaxTokens = 400
	c.MinTailMessages = 2

	// provider 阻塞超过超时 → summarize 失败 → fallback
	block := &blockingSummarizer{release: make(chan struct{})}
	c.Provider = block
	fb := &stubCompactor{}
	c.Fallback = fb
	if _, err := c.Compact(context.Background(), longConversation(6)); err != nil {
		t.Fatalf("超时时应走 fallback, 不返回错误, 实际 %v", err)
	}
	if fb.calls != 1 {
		t.Errorf("摘要超时应触发 fallback, 实际调用 %d 次", fb.calls)
	}
	close(block.release)
}

// blockingSummarizer 阻塞直到 release 关闭，用于验证摘要超时。
type blockingSummarizer struct{ release chan struct{} }

func (b *blockingSummarizer) Generate(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	select {
	case <-b.release:
		return &schema.Message{Role: schema.AssistantRole, Content: "late"}, &schema.Usage{}, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// ---------- 预算内不压缩 ----------

func TestSummarizationCompactor_UnderBudgetNoProviderCall(t *testing.T) {
	s := &stubSummarizer{}
	c := NewSummarizationCompactor(s, 200_000, 2, nil)
	in := longConversation(3)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 0 {
		t.Errorf("预算内不应调用 summarizer, 实际调用 %d 次", s.calls)
	}
	if len(got) != len(in) {
		t.Errorf("预算内应原样返回: 期望 %d 条, 实际 %d 条", len(in), len(got))
	}
}

func TestSummarizationCompactor_NoSystemHeadSkipped(t *testing.T) {
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 1, MinTailMessages: 1}
	in := []schema.Message{{Role: schema.UserRole, Content: strings.Repeat("x", 400)}}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 0 {
		t.Errorf("首条非 system 时不应调用 summarizer, 实际 %d 次", s.calls)
	}
	if len(got) != 1 {
		t.Errorf("首条非 system 时应原样返回, 实际 %d 条", len(got))
	}
}

func TestSummarizationCompactor_TooFewMessagesSkipped(t *testing.T) {
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 1, MinTailMessages: 5}
	in := longConversation(4) // 含 system 共 5 条
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 0 {
		t.Errorf("消息数 <= minTail 时不应调用 summarizer, 实际 %d 次", s.calls)
	}
	if len(got) != len(in) {
		t.Errorf("应原样返回, 实际 %d 条", len(got))
	}
}

// ---------- 压缩结果结构 ----------

func TestSummarizationCompactor_ResultShape(t *testing.T) {
	s := &stubSummarizer{content: "**Goal:** 重构压缩器"}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 400, MinTailMessages: 3}
	in := longConversation(10)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("应调用 summarizer 1 次, 实际 %d 次", s.calls)
	}
	if len(got) != 5 { // system + summary + 3 条 tail
		t.Fatalf("期望 5 条 (system+summary+3 tail), 实际 %d 条", len(got))
	}
	if got[0].Role != schema.SystemRole || got[0].Content != in[0].Content {
		t.Errorf("system 应在首位且保持原样, 实际 %+v", got[0])
	}
	if !strings.Contains(got[1].Content, summaryMarker) {
		t.Errorf("第二条应为带标记的摘要, 实际 %q", got[1].Content)
	}
	if !strings.Contains(got[1].Content, "重构压缩器") {
		t.Errorf("摘要内容应包含 provider 输出, 实际 %q", got[1].Content)
	}
	// tail 原样保留且顺序不变
	for i, want := range in[len(in)-3:] {
		if got[i+2].Content != want.Content || got[i+2].Role != want.Role {
			t.Errorf("tail 第 %d 条不匹配: 期望 %+v, 实际 %+v", i, want, got[i+2])
		}
	}
}

func TestSummarizationCompactor_ResultRepairsOrphanToolResult(t *testing.T) {
	s := &stubSummarizer{content: "**Goal:** summary"}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 500, MinTailMessages: 2}
	in := []schema.Message{{Role: schema.SystemRole, Content: "sys"}}
	for i := 0; i < 8; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("z", 400)})
	}
	// tail 末尾放一条孤立 tool_result（其 assistant tool_call 已被摘要掉）
	in = append(in, schema.Message{Role: schema.UserRole, ToolCallID: "c_gone", Content: "stale"})

	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	for _, m := range got {
		if m.ToolCallID == "c_gone" {
			t.Errorf("孤立 tool_result 应被 repairOrphanedToolPairs 删除, 实际 %+v", m)
		}
	}
}

// ---------- fallback 链路 ----------

func TestSummarizationCompactor_FallbackOnProviderError(t *testing.T) {
	s := &stubSummarizer{err: errors.New("boom")}
	fb := &stubCompactor{out: []schema.Message{{Role: schema.SystemRole, Content: "fallback"}}}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 10, MinTailMessages: 2, Fallback: fb}
	in := longConversation(10)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("provider 失败时应走 fallback, 不返回错误, 实际: %v", err)
	}
	if fb.calls != 1 {
		t.Errorf("应调用 fallback 1 次, 实际 %d 次", fb.calls)
	}
	if len(fb.in) != len(in) {
		t.Errorf("fallback 应收到完整原始消息: 期望 %d 条, 实际 %d 条", len(in), len(fb.in))
	}
	if len(got) != 1 || got[0].Content != "fallback" {
		t.Errorf("应返回 fallback 的结果, 实际 %+v", got)
	}
}

func TestSummarizationCompactor_FallbackOnNilProvider(t *testing.T) {
	fb := &stubCompactor{}
	c := &SummarizationCompactor{MaxTokens: 10, MinTailMessages: 2, Fallback: fb}
	in := longConversation(10)
	if _, err := c.Compact(context.Background(), in); err != nil {
		t.Fatalf("Provider 为 nil 时应走 fallback, 实际: %v", err)
	}
	if fb.calls != 1 {
		t.Errorf("应调用 fallback 1 次, 实际 %d 次", fb.calls)
	}
}

func TestSummarizationCompactor_FallbackOnNilResponse(t *testing.T) {
	fb := &stubCompactor{}
	c := &SummarizationCompactor{Provider: &stubSummarizer{nilResp: true}, MaxTokens: 10, MinTailMessages: 2, Fallback: fb}
	if _, err := c.Compact(context.Background(), longConversation(10)); err != nil {
		t.Fatalf("provider 返回空响应时应走 fallback, 实际: %v", err)
	}
	if fb.calls != 1 {
		t.Errorf("应调用 fallback 1 次, 实际 %d 次", fb.calls)
	}
}

func TestSummarizationCompactor_DefaultFallbackWhenNil(t *testing.T) {
	// Fallback 为 nil 时回落为 TokenBudgetCompactor
	s := &stubSummarizer{err: errors.New("boom")}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 10, MinTailMessages: 2}
	in := longConversation(10)
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	// 回落出的 TokenBudgetCompactor 预算充足（ContextWindow=0 → 160K），故原样返回
	if len(got) != len(in) {
		t.Errorf("应返回 fallback 的结果, 实际 %d 条", len(got))
	}
}

// ---------- 请求构造 ----------

func TestSummarizationCompactor_RequestContainsSystemAndUserMsgs(t *testing.T) {
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 400, MinTailMessages: 2}
	if _, err := c.Compact(context.Background(), longConversation(6)); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if len(s.gotMsgs) != 2 {
		t.Fatalf("summarizer 应收到 system+user 两条, 实际 %d 条", len(s.gotMsgs))
	}
	if s.gotMsgs[0].Role != schema.SystemRole || s.gotMsgs[0].Content != summarySystemPrompt {
		t.Errorf("第一条应为摘要 system prompt, 实际 %+v", s.gotMsgs[0])
	}
	if s.gotMsgs[1].Role != schema.UserRole {
		t.Errorf("第二条应为 user, 实际 %+v", s.gotMsgs[1])
	}
}

func TestSummarizationCompactor_RequestFormatsToolCallAndResult(t *testing.T) {
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 300, MinTailMessages: 1}
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{
			{ID: "c1", Name: "read_tool", Arguments: []byte(`{"path":"a.go"}`)},
		}},
		{Role: schema.UserRole, ToolCallID: "c1", Content: "file body"},
	}
	for i := 0; i < 5; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("q", 400)})
	}
	if _, err := c.Compact(context.Background(), in); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	body := s.gotMsgs[1].Content
	for _, want := range []string{"[tool_call read_tool(c1)]", `{"path":"a.go"}`, "[tool_result c1]: file body"} {
		if !strings.Contains(body, want) {
			t.Errorf("摘要请求应包含 %q, 实际 body: %q", want, body)
		}
	}
}

func TestSummarizationCompactor_SystemPromptNotSummarized(t *testing.T) {
	// system 段不参与摘要：head 从 rest（对话段）起算，summarize 内也跳过 SystemRole。
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 10, MinTailMessages: 2}
	in := []schema.Message{{Role: schema.SystemRole, Content: "secret-system-instruction"}}
	in = append(in, longConversation(6)[1:]...)
	if _, err := c.Compact(context.Background(), in); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	body := s.gotMsgs[1].Content
	if strings.Contains(body, "secret-system-instruction") {
		t.Errorf("system prompt 不应被拼进摘要请求, body: %q", body)
	}
}

func TestSummarizationCompactor_IncrementalSummaryMergesPrevious(t *testing.T) {
	// 上一轮摘要以 UserRole 落在对话段中，本轮应走增量合并模板。
	s := &stubSummarizer{content: "**Goal:** 合并后的摘要"}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 200, MinTailMessages: 1}
	in := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: summaryMarker + "\n旧摘要内容"},
	}
	for i := 0; i < 3; i++ {
		in = append(in, schema.Message{Role: schema.UserRole, Content: strings.Repeat("x", 400)})
	}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	body := s.gotMsgs[1].Content
	if !strings.Contains(body, "<previous-summary>") {
		t.Errorf("已有摘要时应走增量模板, body: %q", body)
	}
	if !strings.Contains(body, "旧摘要内容") {
		t.Errorf("增量请求应携带上一轮摘要内容, body: %q", body)
	}
	// 老摘要消息本身不应再出现在结果里，只保留合并后的新摘要
	for _, m := range got {
		if strings.Contains(m.Content, "旧摘要内容") {
			t.Errorf("上一轮摘要不应残留在结果中: %+v", m)
		}
	}
	if !strings.Contains(got[1].Content, "合并后的摘要") {
		t.Errorf("结果应携带合并后的摘要, 实际 %q", got[1].Content)
	}
}

func TestSummarizationCompactor_FallbackWhenSummaryStillOverBudget(t *testing.T) {
	// 摘要本身过长导致压缩后仍超预算 → 交给 fallback
	s := &stubSummarizer{content: strings.Repeat("s", 8000)}
	fb := &stubCompactor{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 10, MinTailMessages: 2, Fallback: fb}
	if _, err := c.Compact(context.Background(), longConversation(10)); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if fb.calls != 1 {
		t.Errorf("摘要后仍超预算时应走 fallback, 实际调用 %d 次", fb.calls)
	}
}

func TestSummarizationCompactor_PropagatesCallerCancellation(t *testing.T) {
	// ctx 取消时不应再打 LLM，直接向上返回错误
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 10, MinTailMessages: 2}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Compact(ctx, longConversation(6)); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx 已取消时应返回 context.Canceled, 实际 %v", err)
	}
	if s.calls != 0 {
		t.Errorf("ctx 已取消时不应调用 summarizer, 实际 %d 次", s.calls)
	}
}

func TestSummarizationCompactor_CtxPassedToProvider(t *testing.T) {
	// 未取消的 ctx 应透传给 provider（provider 侧能看到 ctx.Done）
	s := &stubSummarizer{}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 400, MinTailMessages: 2}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	if _, err := c.Compact(ctx, longConversation(6)); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("应调用 summarizer 1 次, 实际 %d 次", s.calls)
	}
	if s.gotCtxDone {
		t.Errorf("未取消的 ctx 不应向 provider 暴露已取消状态")
	}
}

func TestSummarizationCompactor_NoSystemHeadStillCompacts(t *testing.T) {
	s := &stubSummarizer{content: "**Goal:** summary"}
	c := &SummarizationCompactor{Provider: s, MaxTokens: 1500, MinTailMessages: 1}
	in := []schema.Message{
		{Role: schema.UserRole, Content: strings.Repeat("x", 4000)},
		{Role: schema.AssistantRole, Content: strings.Repeat("y", 4000)},
		{Role: schema.UserRole, Content: strings.Repeat("z", 4000)},
	}
	got, err := c.Compact(context.Background(), in)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("无 system 段时也应压缩, 实际调用 %d 次", s.calls)
	}
	if len(got) != 2 { // summary + 1 条 tail
		t.Fatalf("期望 2 条 (summary + 1 tail), 实际 %d 条", len(got))
	}
	if !strings.Contains(got[0].Content, summaryMarker) {
		t.Errorf("首条应为摘要, 实际 %q", got[0].Content)
	}
}

func TestSummarizationCompactor_DoesNotMutateInput(t *testing.T) {
	c := &SummarizationCompactor{Provider: &stubSummarizer{}, MaxTokens: 10, MinTailMessages: 2}
	in := longConversation(8)
	snapshot := append([]schema.Message(nil), in...)
	if _, err := c.Compact(context.Background(), in); err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	for i := range in {
		if in[i].Content != snapshot[i].Content || in[i].Role != snapshot[i].Role {
			t.Errorf("Compact 不应修改入参, 第 %d 条: %+v -> %+v", i, snapshot[i], in[i])
		}
	}
}
