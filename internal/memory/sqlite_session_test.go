package memory

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"

	_ "modernc.org/sqlite"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

func TestMain(m *testing.M) {
	log.NewLogger(&zap.Config{
		Level:            zap.NewAtomicLevelAt(zap.ErrorLevel),
		Encoding:         "console",
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	})
	os.Exit(m.Run())
}

func newTestSQLiteSession(t *testing.T) *SQLiteSession {
	t.Helper()
	sess, err := NewSQLiteSession("sqlite-sess", t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteSession 返回错误: %v", err)
	}
	t.Cleanup(func() {
		if err := sess.db.Close(); err != nil {
			t.Errorf("关闭数据库失败: %v", err)
		}
	})
	return sess
}

func TestNewSQLiteSession(t *testing.T) {
	sess := newTestSQLiteSession(t)
	if sess.SessionID() != "sqlite-sess" {
		t.Errorf("期望 sessionID 为 sqlite-sess, 实际 %q", sess.SessionID())
	}
}

func TestSQLiteSession_AddAndGetMessages(t *testing.T) {
	sess := newTestSQLiteSession(t)
	in := []schema.Message{
		{Role: schema.UserRole, Content: "q1"},
		{Role: schema.AssistantRole, Content: "a1"},
		{Role: schema.UserRole, Content: "q2"},
	}
	if err := sess.AddMessages(context.Background(), in); err != nil {
		t.Fatalf("AddMessages 返回错误: %v", err)
	}
	msgs, err := sess.GetMessages(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息, 实际 %d 条", len(msgs))
	}
	want := []string{"q1", "a1", "q2"}
	for i, w := range want {
		if msgs[i].Content != w || msgs[i].Role != in[i].Role {
			t.Errorf("第 %d 条消息错误: 期望 %q/%q, 实际 %q/%q",
				i, w, in[i].Role, msgs[i].Content, msgs[i].Role)
		}
	}
}

func TestSQLiteSession_GetMessages_Limit(t *testing.T) {
	sess := newTestSQLiteSession(t)
	sess.AddMessages(context.Background(), []schema.Message{
		{Role: schema.UserRole, Content: "a"},
		{Role: schema.UserRole, Content: "b"},
		{Role: schema.UserRole, Content: "c"},
	})
	msgs, err := sess.GetMessages(context.Background(), 2)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息, 实际 %d 条", len(msgs))
	}
	if msgs[0].Content != "b" || msgs[1].Content != "c" {
		t.Errorf("期望最近的两条 (b, c), 实际 %+v", msgs)
	}
}

func TestSQLiteSession_ToolCallsRoundTrip(t *testing.T) {
	sess := newTestSQLiteSession(t)
	in := []schema.Message{
		{
			Role:    schema.AssistantRole,
			Content: "calling",
			ToolCalls: []schema.ToolCall{
				{ID: "call_1", Name: "read", Arguments: []byte(`{"path":"x"}`)},
			},
			ToolCallID: "call_1",
		},
	}
	if err := sess.AddMessages(context.Background(), in); err != nil {
		t.Fatalf("AddMessages 返回错误: %v", err)
	}
	msgs, err := sess.GetMessages(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("期望 1 条消息, 实际 %d 条", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "read" {
		t.Errorf("ToolCalls 往返失败: %+v", msgs[0].ToolCalls)
	}
	if msgs[0].ToolCallID != "call_1" {
		t.Errorf("ToolCallID 往返失败: %q", msgs[0].ToolCallID)
	}
}

func TestSQLiteSession_PopMessage(t *testing.T) {
	sess := newTestSQLiteSession(t)
	msg, err := sess.PopMessage(context.Background())
	if err != nil {
		t.Fatalf("空表 PopMessage 返回错误: %v", err)
	}
	if msg != nil {
		t.Errorf("期望空表返回 nil, 实际 %+v", msg)
	}
	sess.AddMessages(context.Background(), []schema.Message{
		{Role: schema.UserRole, Content: "first"},
		{Role: schema.AssistantRole, Content: "last"},
	})
	msg, err = sess.PopMessage(context.Background())
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

func TestSQLiteSession_Clear(t *testing.T) {
	sess := newTestSQLiteSession(t)
	sess.AddMessages(context.Background(), []schema.Message{
		{Role: schema.UserRole, Content: "x"},
		{Role: schema.UserRole, Content: "y"},
	})
	if err := sess.Clear(context.Background()); err != nil {
		t.Fatalf("Clear 返回错误: %v", err)
	}
	msgs, err := sess.GetMessages(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetMessages 返回错误: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("期望 Clear 后为空, 实际 %d 条", len(msgs))
	}
}

func TestSQLiteSession_IndependentSessions(t *testing.T) {
	home := t.TempDir()
	s1, _ := NewSQLiteSession("s1", home)
	s2, _ := NewSQLiteSession("s2", home)
	defer s1.db.Close()
	defer s2.db.Close()

	s1.AddMessages(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "in-s1"}})
	m1, _ := s1.GetMessages(context.Background(), 10)
	m2, _ := s2.GetMessages(context.Background(), 10)
	if len(m1) != 1 {
		t.Errorf("s1 期望 1 条, 实际 %d", len(m1))
	}
	if len(m2) != 0 {
		t.Errorf("s2 期望 0 条, 实际 %d", len(m2))
	}
}
