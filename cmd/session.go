package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// lastSessionFileName 是"上一次会话"的记录文件（位于 ~/.code-agent-go/ 下）。
const lastSessionFileName = "last_session"

// appDirName 与会话库、信任态共用同一个目录约定。
const appDirName = ".code-agent-go"

// maxSessionIDLen 是会话 ID 长度上限。真实值是 uuid（36 字符），
// 这里放宽只为容纳手工指定的 id，同时挡住"把整个文件内容当 ID"的意外。
const maxSessionIDLen = 128

// appDir 返回并确保应用目录存在。
// 之所以用 0700：该目录里放的是会话库、信任态、最近会话记录，
// 默认不应被同机其他用户读走。
func appDir(homeDir string) (string, error) {
	if strings.TrimSpace(homeDir) == "" {
		return "", errors.New("homeDir 为空")
	}
	dir := filepath.Join(homeDir, appDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("创建应用目录失败: %w", err)
	}
	return dir, nil
}

// LastSessionPath 返回记录最近会话的文件路径（不要求文件存在）。
func LastSessionPath(homeDir string) string {
	return filepath.Join(homeDir, appDirName, lastSessionFileName)
}

// SaveLastSession 把本次会话 ID 落盘，供下一次 --continue 使用。
//
// 必须写文件而不是留在内存：--continue 的语义就是跨进程续上一次。
// 采用"写临时文件 + rename"的原子替换：写到一半被中断只会留下临时文件，
// 不会让 last_session 变成半截字符串，从而被 --continue 读成一个不存在的会话。
func SaveLastSession(homeDir, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return errors.New("会话 ID 为空")
	}
	if len(id) > maxSessionIDLen {
		return fmt.Errorf("会话 ID 超过 %d 字符上限", maxSessionIDLen)
	}
	dir, err := appDir(homeDir)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, lastSessionFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0600); err != nil {
		return fmt.Errorf("写入最近会话记录失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换最近会话记录失败: %w", err)
	}
	return nil
}

// LoadLastSession 读取最近一次会话 ID。文件不存在时返回空串（而非错误）：
// 首次运行没有"上一次"是正常状态，不该看起来像失败。
func LoadLastSession(homeDir string) (string, error) {
	if strings.TrimSpace(homeDir) == "" {
		return "", errors.New("homeDir 为空")
	}
	b, err := os.ReadFile(LastSessionPath(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("读取最近会话记录失败: %w", err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", nil
	}
	if len(id) > maxSessionIDLen {
		return "", fmt.Errorf("最近会话记录异常：长度超过 %d 字符上限", maxSessionIDLen)
	}
	return id, nil
}

// SessionPick 是本次运行的会话选择意图（三个来源互斥，见 ResolveSessionID）。
type SessionPick struct {
	// SessionID 来自 -s/--session-id：显式指定一个会话（新建或复用）。
	SessionID string
	// ResumeID 来自 --resume：明确要求复用某个会话的上下文。
	ResumeID string
	// ContinueLast 来自 --continue：续"上一次会话"。
	ContinueLast bool
}

// ResolveSessionID 决定本次运行使用哪个会话。
//
// 优先级：--resume（显式指定） > --continue（续上一次） > -s/--session-id > 新建随机会话。
// --continue 与另外两者同时给出时直接报错：两个来源同时生效意味着"到底续哪个"由实现细节决定，
// 这类歧义宁可在启动期暴露，也不要在运行后才发现上下文接错了。
func ResolveSessionID(homeDir string, opts SessionPick) (string, error) {
	switch {
	case opts.ContinueLast && (opts.SessionID != "" || opts.ResumeID != ""):
		return "", errors.New("--continue 不能与 -s/--session-id 或 --resume 同时使用（同时给出无法确定续哪个，请只保留一个）")
	case opts.ResumeID != "":
		id := strings.TrimSpace(opts.ResumeID)
		if len(id) > maxSessionIDLen {
			return "", fmt.Errorf("--resume 传入的会话 ID 超过 %d 字符上限", maxSessionIDLen)
		}
		return id, nil
	case opts.ContinueLast:
		id, err := LoadLastSession(homeDir)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("没有找到可继续的会话（请先正常运行一次）")
		}
		return id, nil
	case opts.SessionID != "":
		return opts.SessionID, nil
	default:
		return uuid.New().String(), nil
	}
}

// sessionSource 返回本次会话的来源，仅用于启动日志：
// 上下文到底接在哪一的历史上，必须能在日志里一眼看出，否则"为什么它记得上一轮"
// 会变成无法解释的现象。
func sessionSource(o cliOptions) string {
	switch {
	case o.resumeID != "":
		return "--resume"
	case o.continueLast:
		return "--continue"
	case o.sessionID != "":
		return "-s/--session-id"
	default:
		return "new"
	}
}
