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

	homeDir   string
	workDir   string
	appName   string
	sessionID string
}
