// Package sandbox 提供 bash 工具的执行载体抽象：
// 同一个 "在某处执行这条命令" 的契约，既可以由宿主机承接（默认，等价改造前行为），
// 也可以换成一次性容器（production 建议开启），切换只发生在装配阶段。
//
// 为什么不在 BashTool 里直接写 docker 分支：执行载体是部署关注点，
// 与"解析参数、截断输出、翻译超时错误"这些工具语义混在一起会让两者都难以测试。
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner 抽象命令执行体。
type Runner interface {
	// Run 在给定的工作目录下执行命令，返回合并后的 stdout/stderr。
	Run(ctx context.Context, workDir, command string) (string, error)
}

// maxCapturedOutput 是沙箱内命令的输出捕获上限。
// 与工具层的输出截断不同：这里的目的是防止恶意/失控命令把进程内存打爆，
// 因此宁可截断也不能无限缓冲。
const maxCapturedOutput = 8 << 20

// Local 是宿主机执行体：与未启用沙箱时完全一致的行为，没有额外限制。
type Local struct{}

// Run 在宿主机执行 bash -c command。
// 走临时文件而不是内存缓冲：命令输出可能很大（编译日志、测试输出），
// 全量驻留内存会让工具进程的内存占用失去上界。
func (Local) Run(ctx context.Context, workDir, command string) (string, error) {
	tmp, err := os.CreateTemp("", "code-agent-bash-*.log")
	if err != nil {
		return "", fmt.Errorf("创建临时输出文件失败：%w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	c := exec.CommandContext(ctx, "bash", "-c", command)
	c.Dir = workDir
	c.Stdout = tmp
	c.Stderr = tmp
	if err := c.Run(); err != nil {
		return "", translateRunErr(ctx, err)
	}
	return readCapped(tmp.Name())
}

// Docker 是容器执行体：每次调用起一个一次性容器，跑完即删。
//
// 隔离边界（以及它们的代价）：
//   - 文件系统：只有工作目录（可选只读）被挂载，宿主其他路径不可见；
//   - 网络：NetworkDisabled 时 --network none，杜绝数据外传；
//   - 环境变量：完全不继承宿主 env，密钥不会被命令读到；
//   - 生命周期：容器随命令结束销毁，不会残留状态。
//
// 它不是安全边界（容器逃逸、特权镜像依然存在风险），只是显著降低"手滑"和"顺手外传"的概率。
type Docker struct {
	// Image 容器镜像，必须自带 bash。
	Image string
	// NetworkDisabled 是否关闭容器网络。
	NetworkDisabled bool
	// ReadOnlyRootfs 是否以只读方式挂载工作目录。
	ReadOnlyRootfs bool
	// Timeout 容器内命令的超时上限（覆盖 ctx 之外的额外兜底），0 表示不额外限制。
	Timeout time.Duration
}

// workspaceMount 是容器内的工作目录挂载点。
const workspaceMount = "/workspace"

// Command 构造 docker 命令行。单独成方法是为了让"隔离参数是否真的生效"
// 变成可断言的纯函数——这类参数错一处就等于裸跑，靠集成测试很难发现。
func (d Docker) Command(ctx context.Context, workDir, command string) *exec.Cmd {
	args := []string{"run", "--rm"}
	if d.NetworkDisabled {
		args = append(args, "--network", "none")
	}
	mount := workDir + ":" + workspaceMount
	if d.ReadOnlyRootfs {
		mount += ":ro"
	}
	args = append(args,
		"-v", mount,
		"-w", workspaceMount,
		// 不继承宿主环境：--env-file / -e 全都不传，
		// 容器里只有镜像自带的最小环境。
		d.imageOrDefault(),
		"bash", "-c", command,
	)
	return exec.CommandContext(ctx, "docker", args...)
}

func (d Docker) imageOrDefault() string {
	if strings.TrimSpace(d.Image) == "" {
		return "ubuntu:22.04"
	}
	return strings.TrimSpace(d.Image)
}

// Run 在一次性容器内执行命令。
func (d Docker) Run(ctx context.Context, workDir, command string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		// 沙箱是显式开启的开关：依赖缺失时必须 fail closed 并给出可操作提示，
		// 悄悄回落到宿主机执行会让"以为有隔离"变成安全幻觉。
		return "", fmt.Errorf("沙箱已启用但未找到 docker 可执行文件：请安装 Docker，或在配置里关闭 sandbox.enabled")
	}
	absDir, err := absWorkDir(workDir)
	if err != nil {
		return "", err
	}
	runCtx := ctx
	if d.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d.Timeout)
		defer cancel()
	}

	var buf bytes.Buffer
	limited := &limitedWriter{w: &buf, remaining: maxCapturedOutput}
	cmd := d.Command(runCtx, absDir, command)
	cmd.Stdout = limited
	cmd.Stderr = limited
	if err := cmd.Run(); err != nil {
		if out := buf.String(); out != "" {
			return limited.String(), translateRunErr(runCtx, fmt.Errorf("%w\n命令输出：%s", err, out))
		}
		return "", translateRunErr(runCtx, err)
	}
	return limited.String(), nil
}

// absWorkDir 校验并归一化工作目录：docker -v 只接受绝对路径，
// 相对路径会被当成卷名，表现为莫名其妙的 "invalid mount config"。
func absWorkDir(workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("沙箱执行失败：工作目录为空")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("解析工作目录失败：%w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("沙箱挂载的工作目录不可用：%w", err)
	}
	return abs, nil
}

// translateRunErr 把执行失败翻译成带"超时/取消"语义的错误，
// 便于上层 RecoveryManager 注入救援指南（它需要靠关键字识别原因）。
func translateRunErr(ctx context.Context, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("执行命令失败（超时）：%w", err)
	}
	if ctx.Err() == context.Canceled {
		return fmt.Errorf("执行命令失败（已取消）：%w", err)
	}
	return fmt.Errorf("执行命令失败：%w", err)
}

// readCapped 读取命令输出，超出上限的部分丢弃。
func readCapped(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取命令输出失败：%w", err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, maxCapturedOutput)); err != nil {
		return "", fmt.Errorf("读取命令输出失败：%w", err)
	}
	return buf.String(), nil
}

// limitedWriter 是带上限的写入器：超过上限后静默丢弃，
// 而不是让 bytes.Buffer 无限增长。
type limitedWriter struct {
	w         io.Writer
	remaining int
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		l.truncated = true
		return len(p), nil // 假称写入成功，避免命令收到 EPIPE 提前退出
	}
	if len(p) > l.remaining {
		l.truncated = true
		p = p[:l.remaining]
	}
	n, err := l.w.Write(p)
	l.remaining -= n
	return len(p), err
}

func (l *limitedWriter) String() string {
	if buf, ok := l.w.(*bytes.Buffer); ok {
		out := buf.String()
		if l.truncated {
			return out + "\n...[沙箱输出超出捕获上限，已截断]"
		}
		return out
	}
	return ""
}
