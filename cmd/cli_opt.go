package main

var cliOp cliOptions

type cliOptions struct {
	configPath  *string
	enableTools bool
	showVersion bool

	homeDir   string
	workDir   string
	appName   string
	sessionID string
}
