package main

import (
	"encoding/json"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/context_mng"
	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/skill"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
	"github.com/zhuxiufenghust/code-agent-go/internal/tui"

	"path/filepath"

	flag "github.com/spf13/pflag"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"go.uber.org/zap"
)

// loadEnv 依次尝试当前目录与应用级 home 目录下的 .env。
// 只靠 "./.env" 会让"换个目录启动"静默丢掉所有 LLM 配置，
// 症状是第一次调用才报难以定位的传输层错误。
func loadEnv() {
	candidates := []string{".env"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			// 与 sessions.db、skills 的 homeDir 约定保持一致
			filepath.Join(home, ".code-agent-go", ".env"),
			filepath.Join(home, ".env"),
		)
	}
	for _, path := range candidates {
		if err := godotenv.Load(path); err == nil {
			return
		}
	}
	// 一个都没加载成功：此时 logger 尚未初始化，只能用 logfmt 打到 stderr。
	// 不提示的话用户会一路跑到 LLM 调用失败，却不知道原因在这里。
	fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main",
		fmt.Sprintf("未找到 .env（已尝试: %v），LLM 相关环境变量可能缺失", candidates)))
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
	// 日志初始化失败必须 fail fast：日志是不可观测性的基础设施，
	// 静默降级会让排障时"什么都没记录"而毫无察觉。此时 logger 尚未就绪，
	// 只能用 logfmt 打到 stderr 让用户看到可操作信息。
	if err := log.NewLogger(cfg.Log.ToZapConfig()); err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("初始化日志失败: %v", err)))
		os.Exit(1)
	}

	printConfig(cfg)

	// tea.WithAltScreen() 只隔离运行期“可见屏幕”，不会清除终端的滚动历史缓冲区。
	// 若用户在 TUI 运行期间滚动终端（滚轮/滚动条），仍会露出启动前的主屏输出
	// （如配置快照、历史命令）。\033[3J 在进入 alt screen 前擦除滚动历史，
	// 使上滚只能看到空白。\033[2J\033[H 由 alt screen 替代，此处省略。
	// 仅在真实终端下输出，避免污染管道/CI 日志。
	if term := os.Getenv("TERM"); term != "" && term != "dumb" {
		fmt.Fprint(os.Stdout, "\033[3J")
	}
	// LLM 配置必须在"发第一个请求之前"校验：BaseURL/Key 缺失会让 openai-go
	// 构造出空 host 的请求，报错是 `unsupported protocol scheme ""`，
	// 既不提 BaseURL 也不提环境变量名，排查成本极高。
	if err := provider.ValidateConfig(cfg.OpenAI); err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("LLM 配置不可用: %v", err)))
		os.Exit(1)
	}
	pr := provider.NewOpenAIProvider(cfg.OpenAI)
	registry := registerTools(homeDir, workDir)
	// 审批默认开启：只有在配置里显式关闭（tool 段存在且 enableApproval=false）才停用。
	// 内置高危规则（internal/hooks）保证即使不配 dangerCmds 也有底线，
	// 所以"配置缺失"应当等价于"保持防护"，而不是"关闭防护"。
	enableApproval := cfg.Tool == nil || cfg.Tool.EnableApproval
	var approvalMgr *tools.ApprovalManager
	if enableApproval {
		var dangerCmds []string
		if cfg.Tool != nil {
			dangerCmds = cfg.Tool.DangerCmds
		}
		approvalMgr = tools.NewApprovalManager(dangerCmds)
		registry.Use(approvalMgr.WaitForApproval)
		log.Info("工具审批已启用（内置高危规则 + 配置黑名单）", zap.Strings("danger_cmds", dangerCmds))
	} else {
		log.Warn("工具审批已被显式关闭：高危命令将直接执行")
	}
	var sessID string
	if cliOp.sessionID != "" {
		sessID = cliOp.sessionID
	} else {
		sessID = uuid.New().String()
	}

	agent := engine.NewAgentEngine(pr, registry, engine.WithWorkDir(workDir),
		engine.WithHomeDir(homeDir),
		engine.WithSessionID(sessID), engine.WithMaxLoopTurns(cfg.Engine.MaxLoopTurns),
		engine.WithRunTimeout(cfg.Engine.RunTimeout),
		// 压缩配置由 ApplyDefaults 补齐，这里无条件装配（缺配置不再等于关闭压缩）。
		engine.WithCompactor(buildCompactor(cfg, pr)),
	)

	if approvalMgr != nil {
		approvalMgr.SetEmitGetter(agent)
	}

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
	// 补齐生产默认值（如循环轮次上限），避免配置缺项时保护被静默关闭。
	cfg.ApplyDefaults()
	return cfg
}

// buildCompactor 按配置装配压缩器。
// 关键点：token 窗口与消息条数是两种不同单位，不能混用——
// sliding 策略吃的是"消息条数"，token/summarization 吃的是"token 窗口"。
func buildCompactor(cfg *config.Config, pr *provider.OpenAIProvider) context_mng.Compactor {
	c := cfg.Compactor
	switch c.Strategy {
	case config.CompactorStrategySliding:
		return context_mng.NewSlidingWindowCompactor(c.MaxMessages)
	case config.CompactorStrategyToken:
		return context_mng.NewTokenBudgetCompactor(c.ContextWindow)
	default:
		// summarization：LLM 摘要为主，令牌预算兜底（摘要失败/摘要后仍超预算时使用）。
		// fallback 必须与主策略同单位，否则兜底阈值永不触发、降级链路形同虚设。
		return context_mng.NewSummarizationCompactor(pr, c.ContextWindow, c.MinTail,
			context_mng.NewTokenBudgetCompactor(c.ContextWindow))
	}
}

func parseFlags(homeDir string, workDir string) {
	cliOp.homeDir = homeDir
	cliOp.workDir = workDir

	var configPath string
	var enableTools, showVersion bool
	flag.StringVarP(&configPath, "config", "c", "config.json", "Path to the configuration file")
	flag.BoolVarP(&enableTools, "enable-tools", "e", false, "Enable tools")
	flag.StringVarP(&cliOp.sessionID, "session-id", "s", "", "Session ID")
	flag.BoolVarP(&showVersion, "version", "v", false, "Show version information")
	flag.Parse()

	if configPath != "" {
		cliOp.configPath = &configPath
	}
	cliOp.enableTools = enableTools
	cliOp.showVersion = showVersion
}

func registerTools(homeDir, workDir string) tools.Registry {
	register := tools.NewRegistry()
	tools := []tools.Tool{
		tools.NewEditTool(workDir),
		tools.NewReadTool(workDir),
		tools.NewWriteTool(workDir),
		tools.NewBashTool(workDir),
		tools.NewWebSearchTool(),
		skill.NewSkillDispatcher(skill.NewSkillLoader(homeDir, workDir)),
	}
	for _, tool := range tools {
		register.Register(tool)
	}
	return register
}
