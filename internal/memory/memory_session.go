package memory

import (
	"context"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

func NewMemorySession(sessID string) (*MemorySession, error) {
	return &MemorySession{
		sessionID: sessID,
		messages:  []schema.Message{},
	}, nil
}

type MemorySession struct {
	sessionID string
	messages  []schema.Message
	limit     int // max number of messages to keep in memory
}

func (s *MemorySession) SessionID() string {
	return s.sessionID
}

func (s *MemorySession) GetMessages(ctx context.Context, limit int) ([]schema.Message, error) {
	return s.messages, nil
}
func (s *MemorySession) AddMessages(ctx context.Context, msgs []schema.Message) error {
	s.messages = append(s.messages, msgs...)
	if s.limit > 0 && len(s.messages) > s.limit {
		s.messages = s.messages[len(s.messages)-s.limit:]
	}
	return nil
}
func (s *MemorySession) PopMessage(ctx context.Context) (*schema.Message, error) {
	msgs := s.messages
	if len(msgs) == 0 {
		return nil, nil
	}
	lastMsg := msgs[len(msgs)-1]
	s.messages = msgs[:len(msgs)-1]
	return &lastMsg, nil
}

func (s *MemorySession) Clear(ctx context.Context) error {
	s.messages = s.messages[:0]
	return nil
}
