package main

import (
	"encoding/json"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
	"github.com/zhuxiufenghust/code-agent-go/internal/tui"

	"path/filepath"

	flag "github.com/spf13/pflag"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"go.uber.org/zap"
)

func loadEnv() {
	candidates := []string{".env"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".env"))
	}
	for _, path := range candidates {
		if err := godotenv.Load(path); err == nil {
			break
		}
	}
}

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
	loadEnv()

	homeDir, workDir := getDir()
	parseFlags(homeDir, workDir)
	cfg := normalizeConfig()
	log.NewLogger(cfg.Log.ToZapConfig())

	printConfig(cfg)

	// tea.WithAltScreen() 只隔离运行期“可见屏幕”，不会清除终端的滚动历史缓冲区。
	// 若用户在 TUI 运行期间滚动终端（滚轮/滚动条），仍会露出启动前的主屏输出
	// （如配置快照、历史命令）。\033[3J 在进入 alt screen 前擦除滚动历史，
	// 使上滚只能看到空白。\033[2J\033[H 由 alt screen 替代，此处省略。
	// 仅在真实终端下输出，避免污染管道/CI 日志。
	if term := os.Getenv("TERM"); term != "" && term != "dumb" {
		fmt.Fprint(os.Stdout, "\033[3J")
	}
	pr := provider.NewOpenAIProvider(cfg.OpenAI)
	registry := registerTools(homeDir, workDir)

	sessID := uuid.New().String()

	agent := engine.NewAgentEngine(pr, registry, engine.WithWorkDir(workDir),
		engine.WithHomeDir(homeDir),
		engine.WithSessionID(sessID))

	p := tea.NewProgram(tui.New(workDir, cfg.OpenAI.Model, agent),
		tea.WithAltScreen(),
		// 启用鼠标(含滚轮)捕获：滚轮事件会作为 tea.MouseWheelMsg 交给程序，
		// 由 viewport 在应用内滚动，而不是让终端去滚自己的滚动历史（从而看不到启动前输出）。
		tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}

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

func registerTools(homeDir, workDir string) tools.Registry {
	register := tools.NewRegistry()
	tools := []tools.Tool{
		tools.NewEditTool(workDir),
		tools.NewReadTool(workDir),
		tools.NewBashTool(workDir),
		tools.NewWebSearchTool(),
	}
	for _, tool := range tools {
		register.Register(tool)
	}
	return register
}
