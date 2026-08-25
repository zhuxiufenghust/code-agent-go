package memory

import (
	"context"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

func TestNewMemorySession(t *testing.T) {
	sess, err := NewMemorySession("sess-1")
	if err != nil {
		t.Fatalf("NewMemorySession 返回错误: %v", err)
	}
	if sess.SessionID() != "sess-1" {
		t.Errorf("期望 sessionID 为 sess-1, 实际为 %q", sess.SessionID())
	}
	msgs, err := sess.GetMessages(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("期望新会话消息为空, 实际有 %d 条", len(msgs))
	}
}

func TestMemorySession_AddAndGetMessages(t *testing.T) {
	sess, _ := NewMemorySession("s")
	in := []schema.Message{
		{Role: schema.UserRole, Content: "hello"},
		{Role: schema.AssistantRole, Content: "hi"},
	}
	if err := sess.AddMessages(context.Background(), in); err != nil {
		t.Fatalf("AddMessages 返回错误: %v", err)
	}
	msgs, err := sess.GetMessages(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息, 实际 %d 条", len(msgs))
	}
	if msgs[0].Content != "hello" || msgs[1].Content != "hi" {
		t.Errorf("消息顺序或内容错误: %+v", msgs)
	}
}

func TestMemorySession_AddMessages_Empty(t *testing.T) {
	sess, _ := NewMemorySession("s")
	if err := sess.AddMessages(context.Background(), nil); err != nil {
		t.Fatalf("AddMessages(nil) 返回错误: %v", err)
	}
	msgs, _ := sess.GetMessages(context.Background(), 10)
	if len(msgs) != 0 {
		t.Errorf("期望仍为空, 实际 %d 条", len(msgs))
	}
}

func TestMemorySession_Limit(t *testing.T) {
	sess, _ := NewMemorySession("s")
	sess.limit = 2
	in := []schema.Message{
		{Role: schema.UserRole, Content: "a"},
		{Role: schema.UserRole, Content: "b"},
		{Role: schema.UserRole, Content: "c"},
	}
	if err := sess.AddMessages(context.Background(), in); err != nil {
		t.Fatalf("AddMessages 返回错误: %v", err)
	}
	msgs, _ := sess.GetMessages(context.Background(), 100)
	if len(msgs) != 2 {
		t.Fatalf("期望被截断为 2 条, 实际 %d 条", len(msgs))
	}
	if msgs[0].Content != "b" || msgs[1].Content != "c" {
		t.Errorf("期望保留最近两条, 实际 %+v", msgs)
	}
}

func TestMemorySession_PopMessage(t *testing.T) {
	sess, _ := NewMemorySession("s")
	if _, err := sess.PopMessage(context.Background()); err != nil {
		t.Fatalf("空会话 PopMessage 返回错误: %v", err)
	}
	sess.AddMessages(context.Background(), []schema.Message{
		{Role: schema.UserRole, Content: "first"},
		{Role: schema.AssistantRole, Content: "last"},
	})
	msg, err := sess.PopMessage(context.Background())
	if err != nil {
		t.Fatalf("PopMessage 返回错误: %v", err)
	}
	if msg == nil || msg.Content != "last" {
		t.Fatalf("期望弹出 last, 实际 %+v", msg)
	}
	msgs, _ := sess.GetMessages(context.Background(), 100)
	if len(msgs) != 1 || msgs[0].Content != "first" {
		t.Errorf("弹出后剩余消息错误: %+v", msgs)
	}
}

func TestMemorySession_Clear(t *testing.T) {
	sess, _ := NewMemorySession("s")
	sess.AddMessages(context.Background(), []schema.Message{
		{Role: schema.UserRole, Content: "x"},
	})
	if err := sess.Clear(context.Background()); err != nil {
		t.Fatalf("Clear 返回错误: %v", err)
	}
	msgs, _ := sess.GetMessages(context.Background(), 10)
	if len(msgs) != 0 {
		t.Errorf("期望 Clear 后为空, 实际 %d 条", len(msgs))
	}
}
