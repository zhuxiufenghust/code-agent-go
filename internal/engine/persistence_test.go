package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/memory"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// readSessionMessages 直接打开会话库读取已落库的消息。
// 返回 error 版本供 goroutine 内使用（工具 handler 里 t.Fatalf 不安全）。
func readSessionMessagesErr(homeDir, sessID string) ([]schema.Message, error) {
	sess, err := memory.NewSQLiteSession(sessID, homeDir)
	if err != nil {
		return nil, err
	}
	return sess.GetMessages(context.Background(), 100)
}

func readSessionMessages(t *testing.T, homeDir, sessID string) []schema.Message {
	t.Helper()
	msgs, err := readSessionMessagesErr(homeDir, sessID)
	if err != nil {
		t.Fatalf("读取会话消息失败: %v", err)
	}
	return msgs
}

func hasContent(t *testing.T, msgs []schema.Message, want string) {
	t.Helper()
	for _, m := range msgs {
		if strings.Contains(m.Content, want) {
			return
		}
	}
	t.Errorf("会话应持久化 %q, 实际消息: %+v", want, msgs)
}

// TestRunLoop_PersistsOnLLMError 覆盖"LLM 报错 → runLoop 提前 return"这条路径：
// 原先 SaveHistoryContext 只在轮末调用一次，这种路径会把整轮（连用户提问本身）丢掉。
func TestRunLoop_PersistsOnLLMError(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()
	p := &fakeProvider{genErr: errors.New("llm unavailable")}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID),
		engine.WithGenerateRetries(1))

	if err := e.Run(context.Background(), "persist-me"); err == nil {
		t.Fatal("LLM 报错时 Run 应返回错误")
	}

	msgs := readSessionMessages(t, homeDir, sessID)
	// 至少用户的提问要留下来，否则下一次恢复时连自己问了什么都不知道。
	hasContent(t, msgs, "persist-me")
}

// cancelingProvider 首次 Generate 即取消调用方 ctx 并返回 context.Canceled，
// 模拟用户 Ctrl-C / processCtx 超时后排版收尾的场景：
// 此时若沿用同一个已失效的 ctx 写库，收尾落库会直接失败。
type cancelingProvider struct {
	*fakeProvider

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (p *cancelingProvider) Generate(ctx context.Context, history []schema.Message, toolDefs []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil, nil, context.Canceled
}

func TestRunLoop_PersistsOnContextCancel(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &cancelingProvider{fakeProvider: &fakeProvider{}, cancel: cancel}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID),
		engine.WithGenerateRetries(1))

	if err := e.Run(ctx, "interrupted-prompt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望返回 context.Canceled, 实际: %v", err)
	}

	hasContent(t, readSessionMessages(t, homeDir, sessID), "interrupted-prompt")
}

// TestRunLoop_PersistsIncrementally 验证"每一圈立即落库"而不是攒到轮末：
// 借助工具执行的时机（assistant 消息已产生、整轮尚未结束）读取会话库，
// 此时那条携带工具调用的 assistant 消息必须已经在库里。
func TestRunLoop_PersistsIncrementally(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()

	// midLoopMessages 在工具执行时拍摄会话快照。
	var midLoopMessages []schema.Message
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{
				Role:      schema.AssistantRole,
				ToolCalls: []schema.ToolCall{{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}},
			}, usage: &schema.Usage{}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}, usage: &schema.Usage{}},
		},
	}
	reg := &fakeRegistry{
		defs: []schema.ToolDefinition{toolDef("probe", "probe")},
		handler: func(ctx context.Context, call schema.ToolCall) (string, error) {
			msgs, err := readSessionMessagesErr(homeDir, sessID)
			if err != nil {
				return "", err
			}
			midLoopMessages = msgs
			return "probed", nil
		},
	}

	e := engine.NewAgentEngine(p, reg,
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID))
	if err := e.Run(context.Background(), "use probe"); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	if len(midLoopMessages) == 0 {
		t.Fatal("工具执行时未读到任何会话消息")
	}
	var foundToolCall bool
	for _, m := range midLoopMessages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c1" {
				foundToolCall = true
			}
		}
	}
	if !foundToolCall {
		t.Errorf("工具执行时应已落库携带 ToolCall c1 的 assistant 消息, 实际: %+v", midLoopMessages)
	}

	// 整轮结束后工具结果也必须在库里。
	hasContent(t, readSessionMessages(t, homeDir, sessID), "probed")
}

// TestRunLoop_NoDuplicatePersist 是 startIndex 推进的回归用例：
// 持久化改成多次调用后，若边界没有随着写入推进，同一批消息会被重复写进会话。
func TestRunLoop_NoDuplicatePersist(t *testing.T) {
	homeDir, sessID := t.TempDir(), t.Name()
	p := &fakeProvider{
		genScript: []genStep{
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done1"}, usage: &schema.Usage{}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done2"}, usage: &schema.Usage{}},
		},
	}
	e := engine.NewAgentEngine(p, &fakeRegistry{},
		engine.WithHomeDir(homeDir), engine.WithSessionID(sessID))

	if err := e.Run(context.Background(), "first"); err != nil {
		t.Fatalf("第一次 Run 失败: %v", err)
	}
	if err := e.Run(context.Background(), "second"); err != nil {
		t.Fatalf("第二次 Run 失败: %v", err)
	}

	msgs := readSessionMessages(t, homeDir, sessID)
	// system + user1 + assistant1 + user2 + assistant2
	if len(msgs) != 5 {
		t.Fatalf("期望 5 条消息, 实际 %d 条: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != schema.SystemRole {
		t.Errorf("首条应为 system 消息, 实际 %q", msgs[0].Role)
	}
	want := []string{"first", "done1", "second", "done2"}
	for i, w := range want {
		if msgs[i+1].Content != w {
			t.Errorf("第 %d 条消息错误: 期望 %q, 实际 %q", i+1, w, msgs[i+1].Content)
		}
	}
}
