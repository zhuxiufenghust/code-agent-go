package log

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

var logger *zap.Logger

// NewLogger 按配置构建全局 logger。
//
// 返回值而非 panic：日志缺失是"基础设施不可用"，属于调用方能处理的状态——
// 它可以重试、降级或打印明确错误后退出。库里 panic 会剥夺调用方这个选择，
// 而且此刻 logger 还没起来，panic 栈是用户唯一能看到的东西，没有可操作信息。
//
// 建目录职责放在这里而不是 config.ToZapConfig：ToZapConfig 只是一个纯转换函数，
// 不应产生文件系统副作用。
func NewLogger(config *zap.Config) error {
	if config == nil {
		return errors.New("日志配置为空")
	}
	// 让配置里写了尚不存在的日志目录也能正常启动：这是最常见的启动失败原因。
	if err := ensureOutputDirs(config.OutputPaths); err != nil {
		return err
	}
	if err := ensureOutputDirs(config.ErrorOutputPaths); err != nil {
		return err
	}

	var err error
	logger, err = config.Build()
	if err != nil {
		// 兜底：装一个 stdout/stderr logger，避免进程从此进入"日志全部静默丢弃"的状态。
		// 错误仍然返回给调用方决定如何处理。
		fallback := zap.NewProductionConfig()
		fallback.OutputPaths = []string{"stderr"}
		fallback.ErrorOutputPaths = []string{"stderr"}
		if fl, ferr := fallback.Build(); ferr == nil {
			logger = fl.WithOptions(zap.AddCallerSkip(1))
		}
		return fmt.Errorf("构建日志失败: %w", err)
	}
	// log 包对 zap 做了一层封装，跳过 1 帧以定位到真正的调用方代码文件与行号。
	logger = logger.WithOptions(zap.AddCallerSkip(1))
	return nil
}

// ensureOutputDirs 为每个日志文件路径补齐父目录。
func ensureOutputDirs(paths []string) error {
	for _, p := range paths {
		if !isFileSink(p) {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建日志目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// isFileSink 判断该 sink 是否是本地文件路径。
// stdout/stderr 是 zap 的内置别名，带 scheme 的路径由 RegisterSink 注册的自定义 sink 处理，
// 都不需要（也不能）在磁盘上建目录。
func isFileSink(path string) bool {
	p := strings.TrimSpace(path)
	switch p {
	case "", "-", "stdout", "stderr":
		return false
	}
	return !strings.Contains(p, "://")
}

func Info(msg string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	logger.Info(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	logger.Error(msg, fields...)
}
func Debug(msg string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	logger.Debug(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	logger.Warn(msg, fields...)
}
