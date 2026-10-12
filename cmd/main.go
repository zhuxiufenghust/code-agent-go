package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/joho/godotenv"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/context_mng"
	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/headless"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"
	"github.com/zhuxiufenghust/code-agent-go/internal/ltm"
	"github.com/zhuxiufenghust/code-agent-go/internal/observability"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/sandbox"
	"github.com/zhuxiufenghust/code-agent-go/internal/skill"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
	"github.com/zhuxiufenghust/code-agent-go/internal/trust"
	"github.com/zhuxiufenghust/code-agent-go/internal/tui"

	"path/filepath"
	"time"

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

// main 只做一件事：把 run 的返回码交给 os.Exit。
//
// 之所以要多这一层：run 内部持有需要 defer 释放的资源（LTM 库、指标导出器），
// 而 os.Exit 不执行 defer。若在各分支里就地 os.Exit，就会出现
// "开了 --metrics 却在退出时拿不到收尾快照"这种静默数据丢失；
// 把退出码冒泡到这里，所有 defer 都在进程退出前按序跑完。
func main() {
	os.Exit(run())
}

func run() int {
	loadEnv()

	homeDir, workDir := getDir()
	parseFlags(homeDir, workDir)
	cfg := normalizeConfig()
	// 日志初始化失败必须 fail fast：日志是不可观测性的基础设施，
	// 静默降级会让排障时"什么都没记录"而毫无察觉。此时 logger 尚未就绪，
	// 只能用 logfmt 打到 stderr 让用户看到可操作信息。
	if err := log.NewLogger(cfg.Log.ToZapConfig()); err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("初始化日志失败: %v", err)))
		return 1
	}

	printConfig(cfg)

	// tea.WithAltScreen() 只隔离运行期“可见屏幕”，不会清除终端的滚动历史缓冲区。
	// 若用户在 TUI 运行期间滚动终端（滚轮/滚动条），仍会露出启动前的主屏输出
	// （如配置快照、历史命令）。\033[3J 在进入 alt screen 前擦除滚动历史，
	// 使上滚只能看到空白。\033[2J\033[H 由 alt screen 替代，此处省略。
	// 仅在真实终端下输出，避免污染管道/CI 日志；无头模式同样不能写这段控制序列，
	// 否则 `app -p "..." > answer.md` 会得到一个以转义序列开头的文件。
	// LLM 配置必须在"发第一个请求之前"校验：BaseURL/Key 缺失会让 openai-go
	// 构造出空 host 的请求，报错是 `unsupported protocol scheme ""`，
	// 既不提 BaseURL 也不提环境变量名，排查成本极高。
	modelCfg, err := cfg.Models.ResolveModel(cliOp.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("模型选择失败: %v", err)))
		return 1
	}
	if err := provider.ValidateModel(modelCfg); err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("LLM 配置不可用: %v", err)))
		return 1
	}
	pr, err := provider.New(modelCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("构造 Provider 失败: %v", err)))
		return 1
	}
	registry := registerTools(homeDir, workDir, cfg)
	ltmStore := openLTM(homeDir, cfg, registry)
	if ltmStore != nil {
		defer ltmStore.Close()
	}

	// 观测组件：默认 nop（零开销），配置或 --metrics 打开后按周期导出 JSON 指标。
	obs := buildObservability(cfg)
	defer obs.Close()
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
		approvalMgr = tools.NewApprovalManager(dangerCmds, trustOpts(homeDir)...)
		registry.Use(approvalMgr.WaitForApproval)
		log.Info("工具审批已启用（内置高危规则 + 配置黑名单）", zap.Strings("danger_cmds", dangerCmds),
			zap.String("trust_level", approvalMgr.TrustLevel().String()))
	} else {
		log.Warn("工具审批已被显式关闭：高危命令将直接执行")
	}
	var sessID string
	if cliOp.sessionID != "" {
		sessID = cliOp.sessionID
	} else {
		sessID = uuid.New().String()
	}

	engineOpts := []engine.Option{
		engine.WithWorkDir(workDir),
		engine.WithHomeDir(homeDir),
		engine.WithSessionID(sessID), engine.WithMaxLoopTurns(cfg.Engine.MaxLoopTurns),
		engine.WithRunTimeout(cfg.Engine.RunTimeout),
		// 压缩配置由 ApplyDefaults 补齐，这里无条件装配（缺配置不再等于关闭压缩）。
		engine.WithCompactor(buildCompactor(cfg, pr)),
		engine.WithObservability(obs),
		// 上下文窗口与告警水位：token 段是单一来源，ApplyDefaults 已与 compactor 对齐。
		engine.WithTokenBudget(cfg.Token.ContextWindow, cfg.Token.WarnRatio),
	}
	if !cfg.Memory.PersistenceEnabled() {
		engineOpts = append(engineOpts, engine.WithDisablePersistence())
		log.Info("会话持久化已被显式关闭：本次运行不落库任何历史")
	}
	agent := engine.NewAgentEngine(pr, registry, engineOpts...)

	if approvalMgr != nil {
		approvalMgr.SetEmitGetter(agent)
	}

	// 无头判定必须在启动 TUI 之前：一旦 tea.NewProgram 跑起来，
	// 在非 TTY 环境（CI、管道、docker run 不带 -t）里会以
	// "could not open a new TTY" 直接失败，任务根本没机会执行。
	if headless.ShouldRunHeadless(os.Stdin, os.Stdout, cliOp.print) {
		return runHeadless(agent)
	}

	if term := os.Getenv("TERM"); term != "" && term != "dumb" {
		fmt.Fprint(os.Stdout, "\033[3J")
	}

	p := tea.NewProgram(tui.New(workDir, modelCfg.Model, agent),
		tea.WithAltScreen(),
		// 启用鼠标(含滚轮)捕获：滚轮事件会作为 tea.MouseWheelMsg 交给程序，
		// 由 viewport 在应用内滚动，而不是让终端去滚自己的滚动历史（从而看不到启动前输出）。
		tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	return 0
}

// runHeadless 执行一次非交互运行，返回进程退出码。
//
// 退出码约定（脚本据此分支处理）：
//   - 0  运行完成（即便中途有工具失败：工具错误已回灌上下文让模型自愈，不算运行失败）
//   - 1  参数/配置/运行错误
//   - 130 被 SIGINT/SIGTERM 中断（与 shell 惯例一致）
func runHeadless(agent *engine.AgentEngine) int {
	format, err := headless.ParseFormat(cliOp.output)
	if err != nil {
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", err.Error()))
		return 1
	}
	prompt, err := headless.ReadPrompt(flag.Args(), os.Stdin, headless.IsTerminal(os.Stdin) == false)
	if err != nil {
		if errors.Is(err, headless.ErrNoPrompt) {
			fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main",
				"无头模式缺少任务输入：请把任务作为位置参数传入，或通过 stdin 管道输入（例：echo \"hi\" | code-agent-go -p）"))
			return 1
		}
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("读取任务输入失败: %v", err)))
		return 1
	}

	// 中断信号走 context 取消而不是直接退出：这样引擎有机会收尾
	//（关闭事件流、写会话增量），指标也能由上层 defer 导出最后一帧。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := &headless.Runner{
		Out:          os.Stdout,
		Err:          os.Stderr,
		Format:       format,
		ShowThinking: cliOp.showThinking,
	}
	res, err := runner.Run(ctx, agent, prompt)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// 用户主动中断：脚本会用 130 区别于"任务真的失败了"。
			fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", "运行已被中断"))
			return 130
		}
		fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", err.Error()))
		return 1
	}
	// 正文在流式过程中已逐段写出，这里补一个换行，
	// 否则 shell 提示符会紧跟在答案后面（管道场景尤其明显）。
	if format == headless.FormatText && res.Text != "" && !strings.HasSuffix(res.Text, "\n") {
		fmt.Fprintln(os.Stdout)
	}
	return 0
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
func buildCompactor(cfg *config.Config, pr provider.LLMProvider) context_mng.Compactor {
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

// buildObservability 按配置选择观测实现。
// 默认返回 nop：观测不是"越多越好"，在没有采集侧的环境里往 stderr 灌 JSON
// 只会淹没真正有用的错误日志，因此它是一个显式开关。
func buildObservability(cfg *config.Config) observability.Observability {
	o := cfg.Observability
	enabled := o != nil && (o.Enabled || cliOp.metrics)
	if !enabled {
		return observability.Nop()
	}
	exporter := o.Exporter
	var w io.Writer = os.Stderr
	if exporter == config.ExporterStdout {
		w = os.Stdout
	}
	interval := time.Duration(o.DumpIntervalSecs) * time.Second
	log.Info("观测指标导出已启用", zap.String("exporter", exporter), zap.Duration("interval", interval))
	return observability.NewStdout(w, interval)
}

// openLTM 打开长期记忆库并在启用时把 remember/recall 注册进工具表。
// 返回 nil 表示未启用（调用方据此跳过 Close）。
func openLTM(homeDir string, cfg *config.Config, registry tools.Registry) ltm.Store {
	if cfg.LTM == nil || !cfg.LTM.Enabled {
		return nil
	}
	store, err := ltm.NewSQLiteStore(homeDir)
	if err != nil {
		// LTM 属于增强能力：起不来只降级，不能挡住主流程启动。
		log.Error("长期记忆初始化失败，已跳过", zap.Error(err))
		return nil
	}
	for _, tool := range []tools.Tool{
		tools.NewRememberTool(store),
		tools.NewRecallTool(store, cfg.LTM.MaxResults),
	} {
		if err := registry.Register(tool); err != nil {
			log.Warn("注册长期记忆工具失败", zap.String("tool", tool.GetDefinition().Name), zap.Error(err))
			continue
		}
		log.Info("已注册长期记忆工具", zap.String("tool", tool.GetDefinition().Name))
	}
	return store
}

// trustOpts 决定本次进程的信任态。
//
// 优先级：--trust > 持久化的信任态 > Undecided；--approve 只是"本次放行"，不写盘。
// 显式写盘的选择必须留下理由，否则下次启动看到"已授权"却不知道是谁授权的。
func trustOpts(homeDir string) []func(*tools.ApprovalManager) {
	store := trust.NewStore(homeDir)
	opts := []func(*tools.ApprovalManager){tools.WithTrustStore(store)}

	level := store.Load().Level
	switch {
	case cliOp.trustLevel != "":
		parsed, err := trust.ParseLevel(cliOp.trustLevel)
		if err != nil {
			fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("--trust 参数非法: %v", err)))
			os.Exit(1)
		}
		state := trust.State{Level: parsed, Reason: "cli --trust"}
		if err := store.Save(state); err != nil {
			fmt.Fprintln(os.Stderr, logfmt.FormatMsg("main", fmt.Sprintf("持久化信任态失败: %v", err)))
			os.Exit(1)
		}
		level = parsed
	case cliOp.approve:
		level = trust.Trusted
	}
	if level == trust.Trusted {
		log.Warn("当前会话信任态为 trusted：高危命令将不再逐个确认",
			zap.Bool("session_only", cliOp.trustLevel == ""))
	}
	return append(opts, tools.WithTrustLevel(level))
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
	flag.BoolVarP(&cliOp.approve, "approve", "y", false, "本次会话放行高危命令（不写盘的临时 trusted）")
	flag.StringVar(&cliOp.trustLevel, "trust", "", "设置并持久化信任态：trusted|untrusted|undecided")
	flag.BoolVar(&cliOp.metrics, "metrics", false, "导出指标快照与调用 Span（单行 JSON，默认 stderr）")
	flag.StringVarP(&cliOp.model, "model", "m", "", "使用的模型名（对应 models.items[].name，缺省取 models.default）")
	flag.BoolVarP(&cliOp.print, "print", "p", false, "无头运行：不启动 TUI，任务来自参数或 stdin，结果打到标准输出后退出")
	flag.StringVar(&cliOp.output, "output", string(headless.FormatText), "无头模式的输出形态：text（正文走 stdout、诊断走 stderr）| json（全部为单行 JSON）")
	flag.BoolVar(&cliOp.showThinking, "show-thinking", false, "无头模式下同时打印模型推理增量")
	flag.Parse()

	if configPath != "" {
		cliOp.configPath = &configPath
	}
	cliOp.enableTools = enableTools
	cliOp.showVersion = showVersion
}

func registerTools(homeDir, workDir string, cfg *config.Config) tools.Registry {
	register := tools.NewRegistry()
	// 沙箱开启时把 bash 的执行体换成一次性容器；关闭时就是宿主机直跑（Local）。
	bashOpts := []tools.BashOption{}
	if cfg.Sandbox != nil && cfg.Sandbox.Enabled {
		bashOpts = append(bashOpts, tools.WithRunner(sandbox.Docker{
			Image:           cfg.Sandbox.Image,
			NetworkDisabled: cfg.Sandbox.NetworkDisabled,
			ReadOnlyRootfs:  cfg.Sandbox.ReadOnlyRootfs,
			Timeout:         time.Duration(cfg.Sandbox.TimeoutSecs) * time.Second,
		}))
		log.Info("bash 沙箱已启用", zap.String("image", cfg.Sandbox.Image),
			zap.Bool("network_disabled", cfg.Sandbox.NetworkDisabled),
			zap.Bool("read_only_rootfs", cfg.Sandbox.ReadOnlyRootfs))
	}
	tools := []tools.Tool{
		tools.NewEditTool(workDir),
		tools.NewReadTool(workDir),
		tools.NewWriteTool(workDir),
		tools.NewBashTool(workDir, bashOpts...),
		tools.NewWebSearchTool(),
		skill.NewSkillDispatcher(skill.NewSkillLoader(homeDir, workDir)),
	}
	for _, tool := range tools {
		register.Register(tool)
	}
	return register
}
