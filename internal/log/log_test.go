package log

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// testConfig 返回一个只写指定路径的最小日志配置。
func testConfig(paths ...string) *zap.Config {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	cfg.Encoding = "console"
	cfg.OutputPaths = paths
	cfg.ErrorOutputPaths = paths
	return &cfg
}

// TestNewLogger_CreatesMissingDir 验证日志目录不存在时自动创建，而不是 panic。
// 这是最常见的启动失败场景：配置里写了尚不存在的日志目录。
func TestNewLogger_CreatesMissingDir(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "deeply", "nested", "app.log")
	if err := NewLogger(testConfig(logFile)); err != nil {
		t.Fatalf("目录不存在时应自动创建并成功, 实际错误: %v", err)
	}

	Info("after build")

	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("日志文件应已创建: %v", err)
	}
}

// TestNewLogger_ReturnsErrorInsteadOfPanic 验证 sink 不可用时返回错误而非 panic。
// 这里用"父路径是个普通文件"来稳定触发失败——换成权限问题在 root 下会失效。
func TestNewLogger_ReturnsErrorInsteadOfPanic(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备阻塞文件失败: %v", err)
	}

	err := NewLogger(testConfig(filepath.Join(blocker, "app.log")))
	if err == nil {
		t.Fatal("sink 不可用时不应返回 nil")
	}
}

// TestNewLogger_InstallsFallbackOnFailure 验证失败后全局 logger 仍然可用：
// 否则整进程会进入"所有日志静默丢弃"的状态，排障时看不到任何线索。
func TestNewLogger_InstallsFallbackOnFailure(t *testing.T) {
	t.Cleanup(func() { logger = nil })

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备阻塞文件失败: %v", err)
	}

	if err := NewLogger(testConfig(filepath.Join(blocker, "app.log"))); err == nil {
		t.Fatal("期望返回错误")
	}
	if logger == nil {
		t.Fatal("失败后应装上兜底 logger，而不是保持 nil")
	}
	// 打一条确认不会 panic（兜底 logger 已经就绪）。
	Error("fallback logger works")
}

// TestNewLogger_NilConfig 验证 nil 配置返回错误而不是空指针 panic。
func TestNewLogger_NilConfig(t *testing.T) {
	if err := NewLogger(nil); err == nil {
		t.Fatal("nil 配置应返回错误")
	}
}

// TestNewLogger_SkipsNonFileSinks 验证 stdout/stderr 这类内置别名不会被当成文件路径去创建。
// （带 scheme 的 sink 同样跳过，但其能否 Build 取决于 zap 是否注册过该 scheme，
// 因此这里只验证目录创建行为，scheme 判定由 TestIsFileSink 覆盖。）
func TestNewLogger_SkipsNonFileSinks(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if err := NewLogger(testConfig("stdout", "stderr")); err != nil {
		t.Fatalf("内置 stdout/stderr sink 应可用, 实际错误: %v", err)
	}
	// 若误把别名当路径，这里会多出同名文件。
	for _, name := range []string{"stdout", "stderr"} {
		if _, err := os.Stat(name); err == nil {
			t.Errorf("不应把 %q 当成文件路径创建", name)
		}
	}
}

// TestIsFileSink 覆盖 sink 类型判定。
func TestIsFileSink(t *testing.T) {
	cases := map[string]bool{
		"stdout":         false,
		"stderr":         false,
		"-":              false,
		"":               false,
		"kafka://topic":  false,
		"app.log":        true,
		"logs/app.log":   true,
		"/var/log/a.log": true,
	}
	for path, want := range cases {
		if got := isFileSink(path); got != want {
			t.Errorf("isFileSink(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestEnsureOutputDirs_IgnoresBareFilename 验证裸文件名（无目录部分）不会触发建目录。
func TestEnsureOutputDirs_IgnoresBareFilename(t *testing.T) {
	if err := ensureOutputDirs([]string{"app.log", "stdout"}); err != nil {
		t.Errorf("裸文件名不应报错: %v", err)
	}
}

// TestEnsureOutputDirs_ErrorCarriesPath 验证错误信息带上具体的目录，便于用户照着修。
func TestEnsureOutputDirs_ErrorCarriesPath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("准备阻塞文件失败: %v", err)
	}
	target := filepath.Join(blocker, "sub", "app.log")

	err := ensureOutputDirs([]string{target})
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), filepath.Join(blocker, "sub")) {
		t.Errorf("错误信息应包含目标目录, 实际: %v", err)
	}
}
