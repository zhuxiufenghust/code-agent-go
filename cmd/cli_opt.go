package main

var cliOp cliOptions

type cliOptions struct {
	configPath  *string
	enableTools bool
	showVersion bool
	// approve 会话级放行高危命令（等价临时 trusted，不写盘）
	approve bool
	// trustLevel 显式指定信任态并持久化（trusted/untrusted/undecided）
	trustLevel string
	// metrics 打开指标导出（覆盖配置里的 observability.enabled）
	metrics bool
	// model 指定本次使用的模型名（对应 models.items[].name）
	model string
	// print 无头运行：跳过 TUI，直接把结果打到标准输出后退出
	print bool
	// resumeID 显式复用某个会话的上下文（等价 -s，但语义更明确）
	resumeID string
	// continueLast 续上一次会话（记录于 ~/.code-agent-go/last_session）
	continueLast bool
	// output 无头模式的输出形态：text（默认）或 json（单行 JSON 事件流）
	output string
	// showThinking 无头模式下是否打印模型推理增量（默认关闭）
	showThinking bool

	homeDir   string
	workDir   string
	appName   string
	sessionID string
}
