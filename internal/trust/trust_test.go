package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"", Undecided},
		{"trusted", Trusted},
		{"Trusted", Trusted},
		{" yes ", Trusted},
		{"untrusted", Untrusted},
		{"no", Untrusted},
		{"n", Untrusted},
		{"undecided", Undecided},
		{"ask", Undecided},
		{"garbage", Undecided},
	}
	for _, c := range cases {
		got, err := ParseLevel(c.in)
		if c.in == "garbage" {
			if err == nil {
				t.Fatalf("非法输入 %q 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseLevel(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseLevel(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		level      Level
		hasChannel bool
		want       Decision
	}{
		{"未表态且有人工通道 → 询问", Undecided, true, DecisionAsk},
		{"未表态但无人可问 → 拒绝", Undecided, false, DecisionDeny},
		{"已授权 → 放行（即便无人可问）", Trusted, false, DecisionAllow},
		{"保持警惕且有通道 → 仍然询问", Untrusted, true, DecisionAsk},
		{"保持警惕且无人可问 → 拒绝", Untrusted, false, DecisionDeny},
		{"未知等级按最小权限处理", Level("?"), true, DecisionAsk},
	}
	for _, c := range cases {
		if got := Decide(c.level, c.hasChannel); got != c.want {
			t.Fatalf("%s: Decide(%v,%v) = %v, 期望 %v", c.name, c.level, c.hasChannel, got, c.want)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	path := DefaultPath(dir)

	if got := store.Load().Level; got != Undecided {
		t.Fatalf("无文件时应当为 undecided, 实际 %v", got)
	}
	if err := store.Save(State{Level: Trusted, Reason: "cli --trust"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	st := store.Load()
	if st.Level != Trusted || st.Reason != "cli --trust" {
		t.Fatalf("回读内容不符: %+v", st)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("信任态文件未落盘: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if store.Load().Level != Undecided {
		t.Fatal("清理后应回到 undecided")
	}
}

// TestLoadCorruptedFileFailsClosed 是安全底线：
// 信任文件损坏时必须报错且回落到 undecided，绝不能悄悄按 trusted 放行高危命令。
func TestLoadCorruptedFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path)
	if err == nil {
		t.Fatal("损坏文件应当报错")
	}
	if st.Level != Undecided {
		t.Fatalf("损坏文件必须回落到 undecided, 实际 %v", st.Level)
	}
}

func TestSaveNormalizesUnknownLevel(t *testing.T) {
	dir := t.TempDir()
	store := &Store{Path: filepath.Join(dir, "trust.json")}
	if err := store.Save(State{Level: Level("whatever")}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := store.Load().Level; got != Undecided {
		t.Fatalf("未知等级应被归一化为 undecided, 实际 %v", got)
	}
}
