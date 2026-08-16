package main

var cliOp cliOptions

type cliOptions struct {
	configPath  *string
	enableTools bool
	resumeID    string
	showVersion bool

	homeDir string
	workDir string
	appName string
}
