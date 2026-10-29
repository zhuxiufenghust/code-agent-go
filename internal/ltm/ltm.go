// Package ltm 实现长期记忆（Long-Term Memory）的最小闭环：Store 存、Search 取。
//
// 与会话级短期记忆（internal/memory）的区别：
//   - 短期记忆按 session 隔离，随会话结束而不再被调用TOP看的上下文里；
//   - LTM 跨会话共享，存的是"以后还用得上"的事实与偏好，
//     什么时候写、什么时候搜由模型通过 remember/recall 两个工具决定。
//
// 检索实现刻意用 SQLite + LIKE 而不是引入向量检索：
// 先把"能存能搜"的闭环跑通，嵌入式的质量优化（向量索引/重排）留到真正成为瓶颈时。
package ltm

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// dbPath 是 LTM 库相对用户 home 的位置。
const dbPath = ".code-agent-go/data/ltm.db"

const schemaSQL = `
CREATE TABLE IF NOT EXISTS ltm_items (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL DEFAULT 'note',
    content    TEXT    NOT NULL,
    tags       TEXT,
    source     TEXT,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ltm_created ON ltm_items(created_at DESC);
`

// Item 是一条长期记忆。
type Item struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Store 定义了长期记忆的存取契约。
type Store interface {
	// Save 落库一条记忆，并把自增 ID 写回 item。
	Save(ctx context.Context, item *Item) error
	// Search 按关键词检索记忆。query 按空白切词，命中词数多的排在前面；
	// limit <= 0 时由实现给出默认上限。
	Search(ctx context.Context, query string, limit int) ([]Item, error)
	// Delete 删除指定 ID 的记忆。
	Delete(ctx context.Context, id int64) error
	Close() error
}

// SQLiteStore 是 Store 的 SQLite 实现。
type SQLiteStore struct {
	db        *sql.DB
	homeDir   string
	limitRows int
}

// NewSQLiteStore 打开（必要时创建）位于 homeDir 下的 LTM 库并完成建表。
func NewSQLiteStore(homeDir string) (*SQLiteStore, error) {
	file := filepath.Join(homeDir, dbPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("创建长期记忆目录: %w", err)
	}
	// PRAGMA 必须写在 DSN 上（经 db.Exec 执行只对当时借到的那一个连接生效），
	// busy_timeout 要显著小于调用方超时，避免锁竞争把一次写入拖成超时。
	dsn := (&url.URL{
		Scheme: "file",
		Path:   file,
		RawQuery: "_pragma=journal_mode(WAL)" +
			"&_pragma=busy_timeout(1000)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开长期记忆库: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接长期记忆库: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化长期记忆表: %w", err)
	}
	return &SQLiteStore{db: db, homeDir: homeDir, limitRows: 5}, nil
}

func (s *SQLiteStore) Save(ctx context.Context, item *Item) error {
	if item == nil || strings.TrimSpace(item.Content) == "" {
		return fmt.Errorf("长期记忆内容不能为空")
	}
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ltm_items (kind, content, tags, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		defaultKind(item.Kind), strings.TrimSpace(item.Content), joinTags(item.Tags), item.Source, now.Unix())
	if err != nil {
		return fmt.Errorf("写入长期记忆失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取长期记忆 ID 失败: %w", err)
	}
	item.ID = id
	item.CreatedAt = now
	return nil
}

// Search 按分词后的关键词检索。排序规则：命中词数降序 → 时间降序。
// 没有命中任何词时返回空切片而不是全部记录——"随便回忆点什么"比"什么都想不起来"更糟。
func (s *SQLiteStore) Search(ctx context.Context, query string, limit int) ([]Item, error) {
	terms := splitTerms(query)
	if len(terms) == 0 {
		return []Item{}, nil
	}
	if limit <= 0 {
		limit = s.limitRows
	}
	// 先按单条词语 RLIKE/OR 粗筛，再在内存里按命中数精排：
	// 数据量是"用户愿意长期保留的笔记"级别，SQL 里做多重 JOIN 排序收益不抵复杂度。
	where := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+1)
	for _, t := range terms {
		where = append(where, "(lower(content) LIKE ? OR lower(tags) LIKE ?)")
		pattern := "%" + strings.ToLower(t) + "%"
		args = append(args, pattern, pattern)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, content, tags, source, created_at FROM ltm_items WHERE `+
			strings.Join(where, " OR ")+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("检索长期记忆失败: %w", err)
	}
	defer rows.Close()

	items := make([]Item, 0, limit)
	for rows.Next() {
		var (
			it     Item
			tags   sql.NullString
			source sql.NullString
			ts     int64
		)
		if err := rows.Scan(&it.ID, &it.Kind, &it.Content, &tags, &source, &ts); err != nil {
			return nil, fmt.Errorf("读取长期记忆失败: %w", err)
		}
		it.Tags = parseTags(tags.String)
		it.Source = source.String
		it.CreatedAt = time.Unix(ts, 0)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历长期记忆失败: %w", err)
	}
	sortByRelevance(items, terms)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM ltm_items WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除长期记忆失败: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// splitTerms 切词：按空白与常见标点切开，去掉过短的词（单字符会把噪声全捞出来）。
func splitTerms(query string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, raw := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '，' || r == '。' || r == ';'
	}) {
		t := strings.TrimSpace(raw)
		if len([]rune(t)) < 2 {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// score 统计一条记忆命中了多少个查询词。
func score(it Item, terms []string) int {
	hay := strings.ToLower(it.Content + " " + strings.Join(it.Tags, " "))
	n := 0
	for _, t := range terms {
		if strings.Contains(hay, t) {
			n++
		}
	}
	return n
}

func sortByRelevance(items []Item, terms []string) {
	sort.SliceStable(items, func(i, j int) bool {
		si, sj := score(items[i], terms), score(items[j], terms)
		if si != sj {
			return si > sj
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
}

func defaultKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "note"
	}
	return strings.TrimSpace(kind)
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ",")
}

func parseTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}
