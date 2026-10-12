package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type LogConfig struct {
	Level             string   `json:"level" yaml:"level"`
	Encoding          string   `json:"encoding" yaml:"encoding"`
	OutputPaths       []string `json:"outputPaths" yaml:"outputPaths"`
	ErrorOutputPaths  []string `json:"errorOutputPaths" yaml:"errorOutputPaths"`
	Development       bool     `json:"development" yaml:"development"`
	DisableCaller     bool     `json:"disableCaller" yaml:"disableCaller"`
	DisableStacktrace bool     `json:"disableStacktrace" yaml:"disableStacktrace"`
}

type OpenAIOptions struct {
	MaxRetries int `json:"maxRetries" yaml:"maxRetries"`
}

type OpenAIConfig struct {
	Model            string        `json:"model" yaml:"model"`
	BaseURLEnv       string        `json:"baseURLEnv" yaml:"baseURLEnv"`
	ApiKeyEnv        string        `json:"apiKeyEnv" yaml:"apiKeyEnv"`
	Options          OpenAIOptions `json:"options" yaml:"options"`
	IncludeReasoning bool          `json:"includeReasoning" yaml:"includeReasoning"`
}

// Config is the configuration for the application.
type Config struct {
	Log           *LogConfig           `json:"log"`           // 日志相关配置
	OpenAI        *OpenAIConfig        `json:"openai"`        // OpenAI 相关配置
	Engine        *EngineConfig        `json:"engine"`        // AgentEngine 相关配置
	LTM           *LtmConfig           `json:"ltm"`           // LTM 相关配置
	Memory        *MemoryConfig        `json:"memory"`        // Memory 相关配置
	Skill         *SkillConfig         `json:"skill"`         // Skill 相关配置
	Tool          *ToolConfig          `json:"tool"`          // Tool 相关配置
	Compactor     *CompactorConfig     `json:"compactor"`     // Compactor 相关配置
	Models        *ModelsConfig        `json:"models"`        // 可选模型清单（多 Provider 路由）
	Observability *ObservabilityConfig `json:"observability"` // 指标/埋点导出配置
	Token         *TokenConfig         `json:"token"`         // 上下文窗口/水位阈值
	Sandbox       *SandboxConfig       `json:"sandbox"`       // bash 沙箱开关
}

// CompactorConfig 是上下文压缩相关配置。
type CompactorConfig struct {
	// Strategy 压缩策略：summarization（默认，LLM 摘要 + 令牌预算兜底）、
	// token（仅按令牌预算丢弃最旧消息）、sliding（仅保留最近 MaxMessages 条）。
	Strategy string `json:"strategy" yaml:"strategy"`
	// ContextWindow 模型上下文窗口（token）。压缩预算取其 80%。
	ContextWindow int `json:"contextWindow" yaml:"contextWindow"`
	// MinTail 压缩时至少保留的最近消息条数（summarization/token 策略）。
	MinTail int `json:"minTail" yaml:"minTail"`
	// MaxMessages 保留的最大消息条数，仅 sliding 策略使用。
	MaxMessages int `json:"maxMessages" yaml:"maxMessages"`
}

// TokenConfig 是 token 计量与上下文窗口水位相关配置。
type TokenConfig struct {
	// ContextWindow 模型上下文窗口（token）。它是压缩水位与告警水位的基准值，
	// compactor.contextWindow 未显式配置时从此处继承（见 ApplyDefaults）。
	ContextWindow int `json:"contextWindow" yaml:"contextWindow"`
	// WarnRatio 是告警水位占比：上下文估算超过 contextWindow*warnRatio 时打告警日志。
	// 压缩由 Compactor 负责（阈值固定 80%），此处只做"看得见的预警"，二者互不替代。
	// 取值需在 (0,1] 区间，缺省取 DefaultWarnRatio。
	WarnRatio float64 `json:"warnRatio" yaml:"warnRatio"`
}

// SandboxConfig 是 bash 工具的沙箱开关。
type SandboxConfig struct {
	// Enabled 打开后 bash 命令不再直接跑在宿主机，而是跑进一次性容器。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Image 沙箱镜像（必须自带 bash）。缺省 DefaultSandboxImage。
	Image string `json:"image" yaml:"image"`
	// NetworkDisabled 关闭容器网络（--network none），断绝"命令偷偷外传代码/数据"。
	NetworkDisabled bool `json:"networkDisabled" yaml:"networkDisabled"`
	// ReadOnlyRootfs 把工作目录以只读方式挂进容器，禁止写文件。
	ReadOnlyRootfs bool `json:"readOnlyRootfs" yaml:"readOnlyRootfs"`
	// TimeoutSecs 容器内单条命令的超时上限（秒），0 表示沿用 bash 工具自身超时。
	TimeoutSecs int `json:"timeoutSecs" yaml:"timeoutSecs"`
}

// ModelConfig 描述一个可选模型。
// Provider 决定由哪种实现承接；留空时由 Model 前缀推断（openai-/anthropic-/ollama-）。
type ModelConfig struct {
	Name             string `json:"name" yaml:"name"`
	Provider         string `json:"provider" yaml:"provider"`
	Model            string `json:"model" yaml:"model"`
	BaseURLEnv       string `json:"baseURLEnv" yaml:"baseURLEnv"`
	ApiKeyEnv        string `json:"apiKeyEnv" yaml:"apiKeyEnv"`
	IncludeReasoning bool   `json:"includeReasoning" yaml:"includeReasoning"`
	MaxRetries       int    `json:"maxRetries" yaml:"maxRetries"`
}

// ModelsConfig 是模型清单：Default 指定缺省模型名（为空时取列表第一项）。
type ModelsConfig struct {
	Default string        `json:"default" yaml:"default"`
	Items   []ModelConfig `json:"items" yaml:"items"`
}

const (
	// DefaultWarnRatio 是缺省的上下文告警水位：接近窗口 80% 时提示，
	// 与 SummarizationCompactor 的压缩阈值保持一致（压缩前先看得见）。
	DefaultWarnRatio = 0.8
	// DefaultSandboxImage 是缺省沙箱镜像：自带 bash 的最小 Linux 发行版。
	DefaultSandboxImage = "ubuntu:22.04"
)

// 压缩策略枚举值。
const (
	CompactorStrategySummarization = "summarization"
	CompactorStrategyToken         = "token"
	CompactorStrategySliding       = "sliding"
)

const (
	// DefaultContextWindow 是未配置 compactor.contextWindow 时使用的模型窗口（token）。
	DefaultContextWindow = 200_000
	// DefaultMinTail 是未配置 compactor.minTail 时保留的最近消息条数。
	DefaultMinTail = 6
)

type EngineConfig struct {
	WorkDir            string        `json:"workDir" yaml:"workDir"`
	MaxHistoryMsgs     int           `json:"maxHistoryMsgs" yaml:"maxHistoryMsgs"`
	MaxLoopTurns       int           `json:"maxLoopTurns" yaml:"maxLoopTurns"`
	ToolTimeout        time.Duration `json:"toolTimeout" yaml:"toolTimeout"`
	MaxConcurrentTools int           `json:"maxConcurrentTools" yaml:"maxConcurrentTools"`
	// RunTimeout 是单轮流式运行的总时长上限（等待人工审批的时间不计入）。
	// 缺省或 <=0 时由 ApplyDefaults 补上 DefaultRunTimeout。
	// JSON/YAML 走数值（纳秒），如需可读写法可在外部转成纳秒。
	RunTimeout time.Duration `json:"runTimeout" yaml:"runTimeout"`
}

// LtmConfig 是长期记忆（LTM）相关配置。
// LTM 存的是跨会话可复用的事实/偏好，与会话级短期记忆（Memory）互补。
type LtmConfig struct {
	// Enabled 打开后注册 remember/recall 两个工具，让模型能跨会话存取知识。
	// 默认关闭：写入外部模型可控的长期存储属于高权限行为，需显式开启。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// MaxResults 是单次 recall 返回的最大条目数，缺省 DefaultLtmMaxResults。
	MaxResults int `json:"maxResults" yaml:"maxResults"`
}

// ObservabilityConfig 是可观测性导出配置。
type ObservabilityConfig struct {
	// Enabled 打开后按 DumpIntervalSecs 周期导出指标快照，并在 Span 结束时输出 Span 记录。
	// 输出是单行 JSON，便于 jq / 日志采集直接消费。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Exporter 输出目标：stderr（默认，TUI 模式下唯一安全的选择）/ stdout。
	// 不要用 stdout：TUI 正在往 stdout 画界面，插进来的 JSON 行会被当成渲染内容。
	Exporter string `json:"exporter" yaml:"exporter"`
	// DumpIntervalSecs 指标快照的导出周期（秒），缺省 DefaultDumpIntervalSecs。
	DumpIntervalSecs int `json:"dumpIntervalSecs" yaml:"dumpIntervalSecs"`
}

// MemoryConfig 是会话级短期记忆相关配置。
type MemoryConfig struct {
	// Persistence 是否落盘（SQLite）。用指针区分"未配置"与"显式关闭"：
	// 缺省 nil 表示开启，显式 false 表示纯内存会话。
	Persistence *bool `json:"persistence" yaml:"persistence"`
	// MaxHistoryMsgs 单会话保留的最大历史消息条数，缺省沿用 Engine.MaxHistoryMsgs。
	MaxHistoryMsgs int `json:"maxHistoryMsgs" yaml:"maxHistoryMsgs"`
}

// PersistenceEnabled 返回是否启用会话持久化，未配置时按 true（保持向后兼容）。
func (m *MemoryConfig) PersistenceEnabled() bool {
	return m == nil || m.Persistence == nil || *m.Persistence
}

type SkillConfig struct {
	// Add fields for Skill configuration here
}

const (
	// DefaultMaxLoopTurns 是单轮 agent 循环的默认上限：
	// 生产上必须有兜底，否则模型死循环会无限烧钱。配置为 0/缺省时取该值。
	DefaultMaxLoopTurns = 30
	// DefaultRunTimeout 是单轮流式运行（StreamRun）的默认总时长上限。
	// 1 分钟对 CR/重构这类要跑十几轮工具的任务明显不够——模型往往在
	// 最后一轮（正要输出报告时）被掐断，且已付费的上下文因为没走到收尾而浪费。
	// 这里放宽到 10 分钟，仅作兜住"模型/工具卡死"的最后防线。
	DefaultRunTimeout = 10 * time.Minute
	// DefaultLtmMaxResults 是缺省的 LTM 单次检索返回上限。
	DefaultLtmMaxResults = 5
	// DefaultDumpIntervalSecs 是缺省的指标导出周期（秒）。
	DefaultDumpIntervalSecs = 30
)

// ApplyDefaults 为未配置的项补上生产可用的默认值，避免"配置缺一段"就等于"保护关闭"。
// 目前负责：engine 段缺失时补空结构、maxLoopTurns 缺省时补 DefaultMaxLoopTurns。
func (c *Config) ApplyDefaults() {
	if c.Engine == nil {
		c.Engine = &EngineConfig{}
	}
	if c.Engine.MaxLoopTurns <= 0 {
		c.Engine.MaxLoopTurns = DefaultMaxLoopTurns
	}
	// runTimeout 缺省不应等价于"没有上限"：
	// 0/负数会退化成立即到期（newPausableTimeout 拿到非正时长等于一启动就超时），必须补默认值。
	if c.Engine.RunTimeout <= 0 {
		c.Engine.RunTimeout = DefaultRunTimeout
	}
	// token/compactor 共用"上下文窗口"这一个概念，必须收敛到同一个值，
	// 否则压缩阈值与告警水位会各按一套窗口计算，表现飘忽且无从排查。
	// 权威来源是 token.contextWindow；compactor.contextWindow 仅在 token 未配置时被读取
	// （兼容只配了 compactor 的老配置），最终一律回填到 compactor。
	if c.Token == nil {
		c.Token = &TokenConfig{}
	}
	if c.Compactor == nil {
		c.Compactor = &CompactorConfig{}
	}
	if c.Token.ContextWindow <= 0 {
		c.Token.ContextWindow = c.Compactor.ContextWindow
	}
	if c.Token.ContextWindow <= 0 {
		c.Token.ContextWindow = DefaultContextWindow
	}
	if c.Token.WarnRatio <= 0 || c.Token.WarnRatio > 1 {
		c.Token.WarnRatio = DefaultWarnRatio
	}
	c.Compactor.ContextWindow = c.Token.ContextWindow

	// compactor 段缺失不应等价于"关闭压缩"：补默认配置，与各压缩器内部回落保持一致。
	if c.Compactor.Strategy == "" {
		c.Compactor.Strategy = CompactorStrategySummarization
	}
	if c.Compactor.MinTail <= 0 {
		c.Compactor.MinTail = DefaultMinTail
	}

	if c.Sandbox == nil {
		c.Sandbox = &SandboxConfig{}
	}
	if c.Sandbox.Image == "" {
		c.Sandbox.Image = DefaultSandboxImage
	}

	if c.LTM == nil {
		c.LTM = &LtmConfig{}
	}
	if c.LTM.MaxResults <= 0 {
		c.LTM.MaxResults = DefaultLtmMaxResults
	}

	if c.Observability == nil {
		c.Observability = &ObservabilityConfig{}
	}
	if c.Observability.Exporter == "" {
		c.Observability.Exporter = ExporterStderr
	}
	if c.Observability.DumpIntervalSecs <= 0 {
		c.Observability.DumpIntervalSecs = DefaultDumpIntervalSecs
	}

	// models 缺省由 openai 段合成一份等价清单：
	// 这样上层只需面对"选一个 ModelConfig"这一种形态，不必兼容两套入口。
	if c.Models == nil {
		c.Models = &ModelsConfig{}
	}
	if len(c.Models.Items) == 0 {
		if c.OpenAI != nil {
			c.Models.Items = []ModelConfig{{
				Name:             ProviderOpenAI,
				Provider:         ProviderOpenAI,
				Model:            c.OpenAI.Model,
				BaseURLEnv:       c.OpenAI.BaseURLEnv,
				ApiKeyEnv:        c.OpenAI.ApiKeyEnv,
				IncludeReasoning: c.OpenAI.IncludeReasoning,
				MaxRetries:       c.OpenAI.Options.MaxRetries,
			}}
			c.Models.Default = ProviderOpenAI
		}
		if len(c.Models.Items) > 0 && c.Models.Default == "" {
			c.Models.Default = c.Models.Items[0].Name
		}
	}
	if c.Models.Default == "" && len(c.Models.Items) > 0 {
		c.Models.Default = c.Models.Items[0].Name
	}
}

// 支持的指标导出目标。
const (
	ExporterStdout = "stdout"
	ExporterStderr = "stderr"
)

// Provider 别名常量。Provider 字段与 model 前缀推断共用这套名字，
// 避免上层各自写字面量导致"配置里写 ollama、工厂里认 laundry"这类错误。
const (
	ProviderOpenAI     = "openai"
	ProviderCompatible = "openai-compatible"
	ProviderAnthropic  = "anthropic"
	ProviderOllama     = "ollama"
)

// ResolveModel 按名字选出模型配置；name 为空时取 Default（缺省第一项）。
// 找不到时返回错误而不是静默回落到第一个模型——选错模型会直接影响钱和效果。
func (m *ModelsConfig) ResolveModel(name string) (ModelConfig, error) {
	if m == nil || len(m.Items) == 0 {
		return ModelConfig{}, errors.New("models 未配置任何模型")
	}
	if name == "" {
		name = m.Default
	}
	if name == "" {
		name = m.Items[0].Name
	}
	for _, item := range m.Items {
		if item.Name == name {
			if item.Model == "" {
				// name 与 model 同名常见于自定义网关，这里允许；真正不能缺的是_provider_。
				item.Model = name
			}
			return item, nil
		}
	}
	return ModelConfig{}, fmt.Errorf("未找到名为 %q 的模型配置", name)
}

// ToOpenAIConfig 把模型配置落成 Provider 侧熟悉的形态。
func (m ModelConfig) ToOpenAIConfig() *OpenAIConfig {
	return &OpenAIConfig{
		Model:            m.Model,
		BaseURLEnv:       m.BaseURLEnv,
		ApiKeyEnv:        m.ApiKeyEnv,
		IncludeReasoning: m.IncludeReasoning,
		Options:          OpenAIOptions{MaxRetries: m.MaxRetries},
	}
}

// InferProvider 由模型名前缀推断所属 Provider；
// 推断不出时按 OpenAI 兼容处理（绝大多数网关/本地服务都走这条）。
func InferProvider(model string) string {
	switch {
	case strings.HasPrefix(model, ProviderAnthropic+"-"), strings.HasPrefix(model, "claude-"):
		return ProviderAnthropic
	case strings.HasPrefix(model, ProviderOllama+"-"):
		return ProviderOllama
	default:
		return ProviderOpenAI
	}
}

type ToolConfig struct {
	DangerCmds     []string `json:"dangerCmds" yaml:"dangerCmds"`
	EnableApproval bool     `json:"enableApproval" yaml:"enableApproval"`
}

func LoadConfig(path string) (*Config, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = json.Unmarshal(buf, &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseLevel(s string) zapcore.Level {
	var l zapcore.Level
	_ = l.UnmarshalText([]byte(s))
	return l
}

func (c *Config) NewDefaultLog() {
	c.Log = &LogConfig{
		Level:            "info",
		Development:      false,
		Encoding:         "json",
		OutputPaths:      []string{"stderr", "logs/app.log"},
		ErrorOutputPaths: []string{"stderr", "logs/app.log"},
	}
}

func (lc *LogConfig) ToZapConfig() *zap.Config {
	cfg := zap.NewProductionConfig()
	if lc.Encoding != "" {
		cfg.Encoding = lc.Encoding
	}
	if lc.Level != "" {
		cfg.Level = zap.NewAtomicLevelAt(parseLevel(lc.Level))
	}
	if len(lc.OutputPaths) > 0 {
		cfg.OutputPaths = lc.OutputPaths
	}
	if len(lc.ErrorOutputPaths) > 0 {
		cfg.ErrorOutputPaths = lc.ErrorOutputPaths
	}
	cfg.Development = lc.Development
	// 默认开启调用方信息；仅当用户显式关闭时才禁用。
	cfg.DisableCaller = lc.DisableCaller
	cfg.DisableStacktrace = lc.DisableStacktrace

	cfg.EncoderConfig.EncodeTime = func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
		// ⚠️ 注意：time.Time 默认是 UTC，国内业务要显式转本地时区
		t = t.In(time.Local)
		enc.AppendString(t.Format("2006-01-02 15:04:05.000"))
	}
	return &cfg
}
