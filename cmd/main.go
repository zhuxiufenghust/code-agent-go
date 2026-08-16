package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"

	"path/filepath"

	flag "github.com/spf13/pflag"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"go.uber.org/zap"
)

func getDir() (string, string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		errMsg := logfmt.FormatMsg("main", fmt.Sprintf("获取 home 目录失败: %v", err))
		fmt.Println(errMsg)
		panic(errMsg)
	}
	workDir, err := os.Getwd()
	if err != nil {
		errMsg := logfmt.FormatMsg("main", fmt.Sprintf("获取工作目录失败: %v", err))
		fmt.Println(errMsg)
		panic(errMsg)
	}
	return homeDir, workDir
}

func main() {

	homeDir, workDir := getDir()
	parseFlags(homeDir, workDir)
	cfg := normalizeConfig()
	log.NewLogger(cfg.Log.ToZapConfig())

	printConfig(cfg)

}

func printConfig(cfg *config.Config) {
	type printableConfig struct {
		Log map[string]interface{} `json:"log"`
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		errMsg := logfmt.FormatMsg("main", fmt.Sprintf("打印配置文件失败: %v", err))
		fmt.Println(errMsg)
		panic(errMsg)
	}
	log.Debug("after normalize config", zap.String("config", string(b)))
}
func normalizeConfig() *config.Config {
	if cliOp.configPath != nil {
		filePath, err := filepath.Abs(*cliOp.configPath)
		if err != nil {
			errMsg := logfmt.FormatMsg("main", fmt.Sprintf("获取配置文件绝对路径失败: %v", err))
			fmt.Println(errMsg)
			panic(errMsg)
		}
		cliOp.configPath = &filePath
	} else {
		defaultPath := filepath.Join(cliOp.homeDir, cliOp.appName, "conf/config.json")
		cliOp.configPath = &defaultPath
	}

	cfg, err := config.LoadConfig(*cliOp.configPath)
	if err != nil {
		errMsg := logfmt.FormatMsg("main", fmt.Sprintf("加载配置文件失败: %v", err))
		fmt.Println(errMsg)
		panic(errMsg)
	}
	if cfg.Log == nil {
		cfg.NewDefaultLog()
	}
	return cfg
}
func parseFlags(homeDir string, workDir string) {
	cliOp.homeDir = homeDir
	cliOp.workDir = workDir

	var configPath, resumeID string
	var enableTools, showVersion bool
	flag.StringVarP(&configPath, "config", "c", "config.json", "Path to the configuration file")
	flag.BoolVarP(&enableTools, "enable-tools", "e", false, "Enable tools")
	flag.StringVarP(&resumeID, "resume-id", "r", "", "Resume ID")
	flag.BoolVarP(&showVersion, "version", "v", false, "Show version information")
	flag.Parse()

	if configPath != "" {
		cliOp.configPath = &configPath
	}
	cliOp.enableTools = enableTools
	cliOp.resumeID = resumeID
	cliOp.showVersion = showVersion
}
