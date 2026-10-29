package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDockerCommandIsolationFlags 断言"隔离参数真的写进了命令行"。
// 这类参数少写一个就等于裸跑宿主，靠集成测试几乎发现不了，只能在这里钉死。
func TestDockerCommandIsolationFlags(t *testing.T) {
	workDir := "/tmp/proj"
	d := Docker{Image: "ubuntu:22.04", NetworkDisabled: true, ReadOnlyRootfs: true}
	args := d.Command(context.Background(), workDir, "ls").Args

	want := []string{
		"docker",
		"run", "--rm",
		"--network", "none",
		"-v", workDir + ":" + workspaceMount + ":ro",
		"-w", workspaceMount,
		"ubuntu:22.04", "bash", "-c", "ls",
	}
	if len(args) != len(want) {
		t.Fatalf("参数数量不符:\n want %v\n got  %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("第 %d 个参数期望 %q, 实际 %q", i, want[i], args[i])
		}
	}
}

// TestDockerCommandReadWriteMount 说明"未开启只读时才可写"：
// 读写标志写错会让 either 命令全部失败（只读）或沙箱形同虚设（可写）。
func TestDockerCommandReadWriteMount(t *testing.T) {
	d := Docker{Image: "alpine"}
	args := d.Command(context.Background(), "/w", "touch x").Args
	if strings.Contains(strings.Join(args, " "), ":ro") {
		t.Fatalf("未开启只读时不应带 :ro: %v", args)
	}
}

func TestDockerDefaultsImage(t *testing.T) {
	d := Docker{}
	if got := d.imageOrDefault(); got != "ubuntu:22.04" {
		t.Fatalf("缺省镜像不符: %q", got)
	}
	d.Image = "  my/img:1  "
	if got := d.imageOrDefault(); got != "my/img:1" {
		t.Fatalf("镜像应被 trim: %q", got)
	}
}

func TestLocalRunnerExecutesOnHost(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := Local{}.Run(ctx, dir, "echo hello && pwd >/dev/null")
	if err != nil {
		t.Fatalf("本地执行失败: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("输出不符预期: %q", out)
	}
}

// TestLocalRunnerReportsFailure 保证失败命令返回的是错误（而不是空输出 + nil），
// 否则工具层会把失败当成成功，模型永远不知道命令没跑通。
func TestLocalRunnerReportsFailure(t *testing.T) {
	local := Local{}
	if _, err := local.Run(context.Background(), t.TempDir(), "exit 3"); err == nil {
		t.Fatal("命令失败应返回 error")
	}
}

func TestLocalRunnerTranslatesTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := Local{}.Run(ctx, t.TempDir(), "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("期望带超时语义的错误, 实际 %v", err)
	}
}

// TestDockerMissingBinaryFailsClosed 覆盖"开了沙箱但没装 docker"：
// 必须报错而不是悄悄回落到宿主机——否则安全幻觉比没有沙箱更危险。
func TestDockerMissingBinaryFailsClosed(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	_, err := Docker{Image: "ubuntu:22.04"}.Run(context.Background(), t.TempDir(), "echo hi")
	if err == nil || !strings.Contains(err.Error(), "沙箱") {
		t.Fatalf("期望给出明确的沙箱不可用错误, 实际 %v", err)
	}
}

func TestAbsWorkDirRejectsMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-exist")
	if _, err := absWorkDir(missing); err == nil {
		t.Fatal("不存在的目录应报错")
	}
	if _, err := absWorkDir(""); err == nil {
		t.Fatal("空目录应报错")
	}
}

func TestAbsWorkDirAcceptsExisting(t *testing.T) {
	dir, err := os.MkdirTemp("", "sandbox-abs-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	got, err := absWorkDir(dir)
	if err != nil || got != dir {
		t.Fatalf("期望原样返回已存在的绝对路径, got=%q err=%v", got, err)
	}
}

func TestLimitedWriterCapsOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	lw := &limitedWriter{w: buf, remaining: 5}
	if _, err := lw.Write([]byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	if got := lw.String(); !strings.Contains(got, "12345") {
		t.Fatalf("应只保留前 5 字节: %q", got)
	}
	if !strings.Contains(lw.String(), "已截断") {
		t.Fatalf("缺少截断提示: %q", lw.String())
	}
}
