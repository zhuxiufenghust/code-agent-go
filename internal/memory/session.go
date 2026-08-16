package memory

import (
	"context"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// Session 管理单个会话的消息历史与规划状态。
type Session interface {
	SessionID() string
	GetMessages(ctx context.Context, limit int) ([]schema.Message, error)
	AddMessages(ctx context.Context, msgs []schema.Message) error
	PopMessage(ctx context.Context) (*schema.Message, error)
	Clear(ctx context.Context) error

	// GetTodos 返回该会话已持久化的任务列表。无任务时返回 nil, nil。
	// GetTodos(ctx context.Context) ([]planning.TodoItem, error)

	// SaveTodos 原子性保存任务列表（write-replace 语义）。items 为空时清空列表。
	// SaveTodos(ctx context.Context, items []planning.TodoItem) error
}

// SessionInfo 是 Manager.ListSessions 返回的会话元数据。
type SessionInfo struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	MsgCount  int
}
