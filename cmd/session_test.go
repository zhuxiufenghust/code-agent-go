package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoadLastSession(t *testing.T) {
	home := t.TempDir()
	if got, err := LoadLastSession(home); err != nil || got != "" {
		t.Fatalf("首次运行应当为空: %q, %v", got, err)
	}
	if err := SaveLastSession(home, "sess-abc"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := LoadLastSession(home)
	if err != nil || got != "sess-abc" {
		t.Fatalf("读回不符: %q, %v", got, err)
	}
	// 覆盖写：--continue 每次都该拿到最近一次，而不是第一次。
	if err := SaveLastSession(home, "sess-def"); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	if got, _ := LoadLastSession(home); got != "sess-def" {
		t.Fatalf("覆盖后仍读到旧值: %q", got)
	}
	// 原子写不应留下临时文件。
	entries, _ := os.ReadDir(filepath.Join(home, ".code-agent-go"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

func TestLoadLastSessionAppDirIsPrivate(t *testing.T) {
	home := t.TempDir()
	if err := SaveLastSession(home, "sess-1"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	fi, err := os.Stat(filepath.Join(home, ".code-agent-go"))
	if err != nil {
		t.Fatalf("应用目录不存在: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0077 != 0 {
		t.Fatalf("应用目录权限过宽: %o（会被同机其他用户读到会话历史）", perm)
	}
}

func TestResolveSessionID(t *testing.T) {
	home := t.TempDir()
	t.Run("默认新建随机会话", func(t *testing.T) {
		got, err := ResolveSessionID(home, SessionPick{})
		if err != nil || len(got) != 36 {
			t.Fatalf("应新建 uuid: %q, %v", got, err)
		}
	})
	t.Run("-s 显式指定优先于新建", func(t *testing.T) {
		got, _ := ResolveSessionID(home, SessionPick{SessionID: "explicit"})
		if got != "explicit" {
			t.Fatalf("应使用显式传入: %q", got)
		}
	})
	t.Run("--resume 优先于 -s", func(t *testing.T) {
		got, _ := ResolveSessionID(home, SessionPick{SessionID: "explicit", ResumeID: "resumed"})
		if got != "resumed" {
			t.Fatalf("--resume 应优先: %q", got)
		}
	})
	t.Run("--continue 读取上次记录", func(t *testing.T) {
		if err := SaveLastSession(home, "last-one"); err != nil {
			t.Fatalf("准备失败: %v", err)
		}
		got, err := ResolveSessionID(home, SessionPick{ContinueLast: true})
		if err != nil || got != "last-one" {
			t.Fatalf("应续上一次: %q, %v", got, err)
		}
	})
	t.Run("--continue 与 -s/--resume 冲突时报错", func(t *testing.T) {
		if _, err := ResolveSessionID(home, SessionPick{ContinueLast: true, SessionID: "x"}); err == nil {
			t.Fatal("冲突应当报错")
		}
		if _, err := ResolveSessionID(home, SessionPick{ContinueLast: true, ResumeID: "y"}); err == nil {
			t.Fatal("冲突应当报错")
		}
	})
	t.Run("--continue 无历史时给出可操作错误", func(t *testing.T) {
		empty := t.TempDir()
		_, err := ResolveSessionID(empty, SessionPick{ContinueLast: true})
		if err == nil || !strings.Contains(err.Error(), "请先正常运行一次") {
			t.Fatalf("应提示先运行一次: %v", err)
		}
	})
	t.Run("空值保护", func(t *testing.T) {
		// 只有 --continue 需要读 homeDir，其余来源不依赖它。
		if _, err := ResolveSessionID("", SessionPick{ContinueLast: true}); err == nil {
			t.Fatal("homeDir 为空时 --continue 应当报错")
		}
		if err := SaveLastSession(home, "   "); err == nil {
			t.Fatal("空会话 ID 应当报错")
		}
		if err := SaveLastSession(home, strings.Repeat("a", 200)); err == nil {
			t.Fatal("超长会话 ID 应当报错")
		}
	})
}

func TestSessionSource(t *testing.T) {
	cases := []struct {
		op   cliOptions
		want string
	}{
		{cliOptions{resumeID: "x"}, "--resume"},
		{cliOptions{continueLast: true}, "--continue"},
		{cliOptions{sessionID: "x"}, "-s/--session-id"},
		{cliOptions{}, "new"},
	}
	for _, c := range cases {
		if got := sessionSource(c.op); got != c.want {
			t.Fatalf("来源不符: got %q, want %q", got, c.want)
		}
	}
}
