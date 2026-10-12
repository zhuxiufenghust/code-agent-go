// Package trust 管理"人对 Agent 的信任态"。
//
// 与 DangerHook（internal/hooks.MatchDangerous）的分工：
//   - hooks 回答"这条命令危险吗"（事实判断，与用户无关）；
//   - trust 回答"既然危险，要不要问 / 直接放行 / 直接拒绝"（策略判断，取决于用户表态）。
//
// 三态语义：
//   - Undecided：用户还没表态（默认）。危险命令先问；没有交互通道（CI/无头）时拒绝。
//   - Trusted ：用户已授权，直接放行。来源：--approve（仅本次进程）或持久化的选择。
//   - Untrusted：用户明确保持警惕，**每次仍然询问**（不记忆授权），无交互通道时拒绝。
//     Untrusted 不是"直接放行"也不是"永久拒绝"，它表达的是"降低默认权限、别替我做决定"。
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Level 是信任等级。用字符串做底层类型，便于直接落 JSON 且人工可编辑。
type Level string

const (
	// Undecided 未表态：首次遇到危险命令时询问。
	Undecided Level = "undecided"
	// Trusted 已授权：危险命令直接放行。
	Trusted Level = "trusted"
	// Untrusted 保持警惕：危险命令每次都问（无通道则拒绝）。
	Untrusted Level = "untrusted"
)

// ParseLevel 解析信任等级，空串按 Undecided 处理（“没配”等价于“没表态”）。
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return Undecided, nil
	case "trusted", "trust", "yes", "y", "allow":
		return Trusted, nil
	case "untrusted", "distrust", "no", "n", "deny":
		return Untrusted, nil
	case "undecided", "ask", "prompt":
		return Undecided, nil
	default:
		return Undecided, fmt.Errorf("未知的信任等级 %q（可选: trusted/untrusted/undecided）", s)
	}
}

// String 返回等级的可读/可持久化形式。
func (l Level) String() string {
	if l == Trusted || l == Untrusted {
		return string(l)
	}
	return string(Undecided)
}

// State 是被持久化的信任态快照。
type State struct {
	Level Level `json:"level"`
	// Reason 记录这次表态的来源（如 "cli --approve"、"--trust trusted"），便于回溯。
	Reason string `json:"reason,omitempty"`
	// UpdatedAt 是最后一次表态时间。
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Decision 是对"这条危险命令该怎么办"的策略结论。
type Decision string

const (
	// DecisionAllow 直接放行。
	DecisionAllow Decision = "allow"
	// DecisionAsk 弹人工确认。
	DecisionAsk Decision = "ask"
	// DecisionDeny 直接拒绝。
	DecisionDeny Decision = "deny"
)

// Decide 依据信任态与"是否存在人工审批通道"给出处置结论。
//
// hasApprovalChannel 为 false 表示非交互场景（无头/CI/没有 TUI）：
// 此时无论是 Undecided 还是 Untrusted 都必须 deny——问不到人的时候，
// 唯一安全的选项是拒绝高危操作（只有 Trusted 才会在无人值守时执行）。
func Decide(level Level, hasApprovalChannel bool) Decision {
	switch level {
	case Trusted:
		return DecisionAllow
	case Untrusted:
		if hasApprovalChannel {
			return DecisionAsk
		}
		return DecisionDeny
	default: // Undecided 及任何未识别值（保持最小权限）
		if hasApprovalChannel {
			return DecisionAsk
		}
		return DecisionDeny
	}
}

// trustFilePath 是信任态文件相对于用户 home 的位置，
// 与会话库（.code-agent-go/data/sessions.db）保持同一套目录约定。
const trustFilePath = ".code-agent-go/trust.json"

// DefaultPath 返回 trust.json 的默认路径。
func DefaultPath(homeDir string) string { return filepath.Join(homeDir, trustFilePath) }

// Load 读取持久化的信任态。文件不存在返回 Undecided——
// "从没选过"不是一个需要报错的状态。文件损坏则必须报错：
// 其内容直接决定高危命令是否放行，静默回落到 Trusted 会酿成事故。
func Load(path string) (State, error) {
	buf, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{Level: Undecided}, nil
	}
	if err != nil {
		return State{Level: Undecided}, fmt.Errorf("读取信任态文件 %s: %w", path, err)
	}
	var st State
	if err := json.Unmarshal(buf, &st); err != nil {
		return State{Level: Undecided}, fmt.Errorf("解析信任态文件 %s: %w", path, err)
	}
	level, err := ParseLevel(string(st.Level))
	if err != nil {
		return State{Level: Undecided}, fmt.Errorf("信任态文件 %s: %w", path, err)
	}
	st.Level = level
	return st, nil
}

// Save 原子地写入信任态：先写临时文件再 rename，
// 避免进程被 kill 时留下半截 JSON（下一次读取会永久失败）。
func Save(path string, st State) error {
	st.Level = Level(st.Level.String()) // 归一化未知值
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now()
	}
	buf, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化信任态: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建信任态目录: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入信任态临时文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落盘信任态: %w", err)
	}
	return nil
}

// Store 绑定到一个具体路径的信任态读写器。
type Store struct {
	// Path 是信任态文件路径。空值表示不做持久化（纯内存语义）。
	Path string
}

// NewStore 基于用户 home 目录构造默认的信任态存储。
func NewStore(homeDir string) *Store { return &Store{Path: DefaultPath(homeDir)} }

// Load 读取当前持久化的信任态；文件缺失时返回 Undecided。
func (s *Store) Load() State {
	if s == nil || s.Path == "" {
		return State{Level: Undecided}
	}
	st, err := Load(s.Path)
	if err != nil {
		// 读取失败按"未表态"处理，绝不静默升到 Trusted。
		return State{Level: Undecided}
	}
	return st
}

// Save 持久化信任态变更。Path 为空时是空操作（显式支持"只用会话级授权"的用法）。
func (s *Store) Save(st State) error {
	if s == nil || s.Path == "" {
		return nil
	}
	return Save(s.Path, st)
}

// Clear 删除持久化文件，回到"从未表态"。文件不存在不算错误。
func (s *Store) Clear() error {
	if s == nil || s.Path == "" {
		return nil
	}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除信任态文件: %w", err)
	}
	return nil
}
