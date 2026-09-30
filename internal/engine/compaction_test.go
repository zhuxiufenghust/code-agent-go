package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/memory"
	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// summaryMarker 与 context_mng 内部的摘要标记保持一致（未导出，这里以字面量固定）。
const summaryMarker = "[Conversation Summary]"

// stubCompactor 记录每次压缩的入参，并按 out/err 决定返回什么。
type stubCompactor struct {
	mu        sync.Mutex
	calls     int
	histories [][]schema.Message
	// out 为 nil 表示原样返回（模拟"预算内无需压缩"）。
	out func(msgs []schema.Message) []schema.Message
	err error
}

func (c *stubCompactor) Compact(_ context.Context, msgs []schema.Message) ([]schema.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.histories = append(c.histories, copyMessages(msgs))
	if c.err != nil {
		return nil, c.err
	}
	if c.out != nil {
		return c.out(msgs), nil
	}
	return msgs, nil
}

// dropOldestAndSummarize 模拟 SummarizationCompactor：保留 system + 最近 1 条，
// 中间的历史替换为一条摘要消息。
func dropOldestAndSummarize(msgs []schema.Message) []schema.Message {
	if len(msgs) < 3 {
		return msgs
	}
	out := []schema.Message{msgs[0]}
	out = append(out, schema.Message{Role: schema.UserRole, Content: summaryMarker + "\n压缩后的历史"})
	out = append(out, msgs[len(msgs)-1])
	return out
}

// seedSessionHistory 先跑一轮"无压缩器"的运行，让会话落下一小段历史，
// 使后续运行的 llmContext 里存在可被压缩的旧消息。
func seedSessionHistory(t *testing.T, homeDir, sessID, prompt, reply string) {
	t.Helper()
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: reply}, usage: &schema.Usage{}},
		},
	}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID))
	if err := e.Run(context.Background(), prompt); err != nil {
		t.Fatalf("预置历史失败: %v", err)
	}
}

// TestCompactor_CompactedContextIsWhatLLMSees 验证压缩结果真的喂给了 LLM。
func TestCompactor_CompactedContextIsWhatLLMSees(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()
	seedSessionHistory(t, homeDir, sessID, "first", "done1")

	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done2"}, usage: &schema.Usage{}},
		},
	}
	compactor := &stubCompactor{out: dropOldestAndSummarize}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID),
		engine.WithCompactor(compactor))

	if err := e.Run(context.Background(), "second"); err != nil {
		t.Fatalf("Run 不应失败: %v", err)
	}
	if compactor.calls == 0 {
		t.Fatal("compactor 未被调用")
	}
	if len(p.genHistories) != 1 {
		t.Fatalf("期望 1 次 LLM 调用, 实际 %d", len(p.genHistories))
	}
	got := p.genHistories[0]
	// system + 摘要 + 最近一条（当前用户输入）
	if len(got) != 3 {
		t.Fatalf("压缩后上下文应为 system+摘要+最近一条, 实际 %d 条", len(got))
	}
	if !strings.Contains(got[1].Content, summaryMarker) {
		t.Errorf("第二条应为摘要消息, 实际 %q", got[1].Content)
	}
	if got[2].Content != "second" {
		t.Errorf("最近一条用户输入应保留, 实际 %q", got[2].Content)
	}
}

// TestCompactor_ErrorKeepsLoopRunning 验证压缩失败时沿用原上下文继续跑，不中断循环。
func TestCompactor_ErrorKeepsLoopRunning(t *testing.T) {
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}, usage: &schema.Usage{}},
		},
	}
	compactor := &stubCompactor{err: errors.New("boom")}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(t.TempDir()), engine.WithSessionID(t.Name()),
		engine.WithCompactor(compactor))

	if err := e.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("压缩失败不应中断主流程, 实际: %v", err)
	}
	if len(p.genHistories) != 1 {
		t.Fatalf("期望仍完成 1 次 LLM 调用, 实际 %d", len(p.genHistories))
	}
	got := p.genHistories[0]
	if len(got) != 2 {
		t.Errorf("压缩失败时应沿用原始上下文 (system+user), 实际 %d 条: %+v", len(got), got)
	}
	for _, m := range got {
		if strings.Contains(m.Content, summaryMarker) {
			t.Errorf("压缩失败时不应出现摘要消息: %+v", m)
		}
	}
}

// TestCompactor_NoEventWhenNothingCompacted 验证"跑了一遍但什么都没删"不产生压缩事件。
func TestCompactor_NoEventWhenNothingCompacted(t *testing.T) {
	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}, usage: &schema.Usage{}},
		},
	}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(t.TempDir()), engine.WithSessionID(t.Name()),
		engine.WithCompactor(&stubCompactor{})) // out=nil → 原样返回

	ch, err := e.StreamRun(context.Background(), "hello")
	if err != nil {
		t.Fatalf("StreamRun 失败: %v", err)
	}
	for evt := range ch {
		if evt.Type == engine.EventCompaction {
			t.Errorf("未真正压缩时不应产生压缩事件: %+v", evt)
		}
	}
}

// TestCompaction_EmitsEvent 验证有效压缩会向客户端推送一次压缩事件。
func TestCompaction_EmitsEvent(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()
	seedSessionHistory(t, homeDir, sessID, "first", "done1")

	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done2"}, usage: &schema.Usage{}},
		},
	}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID),
		engine.WithCompactor(&stubCompactor{out: dropOldestAndSummarize}))

	ch, err := e.StreamRun(context.Background(), "second")
	if err != nil {
		t.Fatalf("StreamRun 失败: %v", err)
	}
	var events int
	for evt := range ch {
		if evt.Type != engine.EventCompaction {
			continue
		}
		events++
		data, ok := evt.Data.(report.CompactionData)
		if !ok {
			t.Fatalf("压缩事件载荷类型不符: %T", evt.Data)
		}
		if data.MsgsAfter >= data.MsgsBefore {
			t.Errorf("压缩后消息数应减少: before=%d after=%d", data.MsgsBefore, data.MsgsAfter)
		}
		if data.Turn <= 0 {
			t.Errorf("压缩事件应携带轮次, 实际 %d", data.Turn)
		}
	}
	if events != 1 {
		t.Fatalf("期望 1 次压缩事件, 实际 %d", events)
	}
}

// TestCompaction_PersistsNewMessages 是"压缩破坏持久化边界"的回归用例：
// 压缩会删掉（并替换）已落库的历史消息，若仍按压缩前的 startIndex 增量保存，
// 本轮新增的消息会漏存或存到错位片段。
func TestCompaction_PersistsNewMessages(t *testing.T) {
	homeDir := t.TempDir()
	sessID := t.Name()

	// 第一次运行：无压缩器，会话里落下 system + user1 + assistant1。
	p1 := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done1"}, usage: &schema.Usage{}},
		},
	}
	e1 := engine.NewAgentEngine(p1, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID))
	if err := e1.Run(context.Background(), "first"); err != nil {
		t.Fatalf("第一次运行失败: %v", err)
	}

	// 第二次运行：开启压缩。历史被裁剪为 system + 摘要 + 当前 user 输入。
	p2 := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done2"}, usage: &schema.Usage{}},
		},
	}
	e2 := engine.NewAgentEngine(p2, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID),
		engine.WithCompactor(&stubCompactor{out: dropOldestAndSummarize}))
	if err := e2.Run(context.Background(), "second"); err != nil {
		t.Fatalf("第二次运行失败: %v", err)
	}

	sess, err := memory.NewSQLiteSession(sessID, homeDir)
	if err != nil {
		t.Fatalf("打开会话失败: %v", err)
	}
	msgs, err := sess.GetMessages(context.Background(), 100)
	if err != nil {
		t.Fatalf("读取会话消息失败: %v", err)
	}

	want := []string{summaryMarker, "second", "done2"}
	for _, w := range want {
		var found bool
		for _, m := range msgs {
			if strings.Contains(m.Content, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("会话应持久化 %q, 实际消息: %+v", w, msgs)
		}
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "first") || strings.Contains(m.Content, "done1") {
			t.Errorf("压缩掉的历史不应残留在会话中: %+v", m)
		}
	}
}
