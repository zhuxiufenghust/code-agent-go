package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

const dbPath = ".code-agent-go/data/sessions.db"
const schemaSQL = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT    NOT NULL,
    role         TEXT    NOT NULL,
    content      TEXT    NOT NULL,
    tool_calls   TEXT,
    tool_call_id TEXT,
    created_at   INTEGER NOT NULL,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);

CREATE TABLE IF NOT EXISTS session_todos (
    id          TEXT    NOT NULL,
    session_id  TEXT    NOT NULL,
    content     TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending',
    position    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, session_id),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_todos_session ON session_todos(session_id);
`

func NewSQLiteSession(sessID string, homeDir string) (*SQLiteSession, error) {
	sess := &SQLiteSession{
		sessionID: sessID,
		db:        nil,
		homeDir:   homeDir,
	}
	if err := sess.init(); err != nil {
		return nil, err
	}
	return sess, nil
}

type SQLiteSession struct {
	sessionID string
	db        *sql.DB
	homeDir   string
}

func (s *SQLiteSession) SessionID() string {
	return s.sessionID
}

func (s *SQLiteSession) init() error {
	dbFilePath := filepath.Join(s.homeDir, dbPath)
	if err := os.MkdirAll(filepath.Dir(dbFilePath), 0700); err != nil {
		return fmt.Errorf("创建数据库目录: %w", err)
	}
	db, err := sql.Open("sqlite", dbFilePath)
	if err != nil {
		return fmt.Errorf("打开数据库: %w", err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return fmt.Errorf("设置 pragma: %w", err)
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return fmt.Errorf("初始化 schema: %w", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO sessions (id, created_at, updated_at) VALUES (?, ?, ?)`,
		s.sessionID, now, now,
	); err != nil {
		db.Close()
		return fmt.Errorf("初始化会话记录: %w", err)
	}
	s.db = db
	return nil
}

func (s *SQLiteSession) GetMessages(ctx context.Context, limit int) ([]schema.Message, error) {
	rows, err := s.db.Query("SELECT role, content, tool_calls, tool_call_id FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT ?",
		s.sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询消息: %w", err)
	}
	msgs := []schema.Message{}
	for rows.Next() {
		var (
			roleStr     string
			content     string
			toolCallsJS sql.NullString
			toolCallID  sql.NullString
		)
		if err := rows.Scan(&roleStr, &content, &toolCallsJS, &toolCallID); err != nil {
			return nil, fmt.Errorf("扫描消息: %w", err)
		}
		msg := schema.Message{
			Role:    schema.Role(roleStr),
			Content: content,
		}
		if toolCallsJS.Valid && toolCallsJS.String != "" {
			if err := json.Unmarshal([]byte(toolCallsJS.String), &msg.ToolCalls); err != nil {
				return nil, fmt.Errorf("反序列化 tool_calls: %w", err)
			}
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代消息: %w", err)
	}

	if limit > 0 {
		// 反转 DESC 结果为升序
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
	}
	return msgs, nil
}
func (s *SQLiteSession) AddMessages(ctx context.Context, msgs []schema.Message) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务: %w", err)
	}
	var dbErr error
	defer func() {
		if dbErr != nil {
			tx.Rollback()
		}
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	now := time.Now().Unix()
	for _, msg := range msgs {
		toolCallsJS := "[]"
		if len(msg.ToolCalls) > 0 {
			if b, err := json.Marshal(msg.ToolCalls); err == nil {
				toolCallsJS = string(b)
			} else {
				dbErr = fmt.Errorf("序列化 tool_calls: %w", err)
				return dbErr
			}
		}

		if _, err := tx.Exec(
			"INSERT INTO messages (session_id, role, content, tool_calls, tool_call_id, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			s.sessionID, msg.Role, msg.Content, toolCallsJS, msg.ToolCallID, now,
		); err != nil {
			dbErr = fmt.Errorf("插入消息: %w", err)
			return dbErr
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, s.sessionID)
	if err != nil {
		dbErr = fmt.Errorf("更新会话时间戳: %w", err)
		return dbErr
	}

	if err := tx.Commit(); err != nil {
		dbErr = fmt.Errorf("提交事务: %w", err)
		return dbErr
	}

	return nil
}
func (s *SQLiteSession) PopMessage(ctx context.Context) (*schema.Message, error) {
	row := s.db.QueryRow("SELECT id, role, content, tool_calls, tool_call_id FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 1", s.sessionID)
	var (
		roleStr     string
		content     string
		toolCallsJS sql.NullString
		toolCallID  sql.NullString
	)
	var id int64
	err := row.Scan(&id, &roleStr, &content, &toolCallsJS, &toolCallID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("扫描消息: %w", err)
	}
	msg := &schema.Message{
		Role:    schema.Role(roleStr),
		Content: content,
	}
	if toolCallsJS.Valid && toolCallsJS.String != "" {
		if err := json.Unmarshal([]byte(toolCallsJS.String), &msg.ToolCalls); err != nil {
			return nil, fmt.Errorf("反序列化 tool_calls: %w", err)
		}
	}
	if toolCallID.Valid {
		msg.ToolCallID = toolCallID.String
	}
	// 删除消息失败，只简单记录日志
	res, err := s.db.Exec("DELETE FROM messages WHERE id = ?", id)
	if err != nil {
		log.Warn("删除消息失败", zap.Error(err), zap.Int64("message_id", id))
		return nil, nil
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		log.Warn("删除消息失败 affected失败", zap.Error(err), zap.Int64("message_id", id))
		return nil, nil
	}
	if rowsAffected == 0 {
		log.Warn("删除消息失败 affected失败", zap.Error(err), zap.Int64("message_id", id),
			zap.Int("count", int(rowsAffected)))
		return nil, nil
	}
	return msg, nil
}
func (s *SQLiteSession) Clear(ctx context.Context) error {
	res, err := s.db.Exec("DELETE FROM messages WHERE session_id = ?", s.sessionID)
	if err != nil {
		return fmt.Errorf("清空消息失败: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("获取清空消息行数失败: %w", err)
	}
	if rowsAffected == 0 {
		log.Warn("清空消息失败 affected失败", zap.String("session_id", s.sessionID),
			zap.Int("count", int(rowsAffected)))
	}
	return nil
}
