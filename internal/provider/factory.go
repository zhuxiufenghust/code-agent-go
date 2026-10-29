package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
)

// FactoryFunc 由一份模型配置构造出 LLMProvider。
// 之所以不直接传 *OpenAIConfig：多 Provider 各自的入参形态不同，
// 用"模型配置"做统一入口，新 Provider 只需再加一个注册项。
type FactoryFunc func(cfg config.ModelConfig) (LLMProvider, error)

var (
	factoryMu sync.RWMutex
	factories = map[string]FactoryFunc{}
)

func init() {
	openAIFactory := func(cfg config.ModelConfig) (LLMProvider, error) {
		return NewOpenAIProvider(cfg.ToOpenAIConfig()), nil
	}
	anthropicFactory := func(cfg config.ModelConfig) (LLMProvider, error) {
		return NewAnthropicProvider(cfg.ToAnthropicConfig()), nil
	}
	// OpenAI 兼容层是目前唯一的实现：自建网关、大部分云厂商、
	// 以及 ollama 的 /v1 端点都走这条路径。
	Register(config.ProviderOpenAI, openAIFactory)
	Register(config.ProviderCompatible, openAIFactory)
	Register(config.ProviderOllama, openAIFactory)
	// Anthropic 用的是 Messages API（不是 /chat/completions），
	// 必须先注册实现，否则 claude- 前缀的模型会被路由到 OpenAI 兼容层而 404。
	Register(config.ProviderAnthropic, anthropicFactory)
}

// Register 注册（或覆盖）一个 provider 实现。
func Register(name string, f FactoryFunc) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	factories[strings.ToLower(name)] = f
}

// Lookup 返回已注册的实现。
func Lookup(name string) (FactoryFunc, bool) {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	f, ok := factories[strings.ToLower(name)]
	return f, ok
}

// RegisteredProviders 返回已注册的 provider 名（排序后，便于直接出现在报错里）。
func RegisteredProviders() []string {
	factoryMu.RLock()
	defer factoryMu.RUnlock()
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// New 是模型路由入口：按 cfg.Provider 选择实现，Provider 为空时由模型名前缀推断。
// 未注册的实现必须报错——悄悄回落到 OpenAI 兼容会让 Anthropic 这类
// 不兼容 /chat/completions 的服务以一个莫名其妙的 404 失败，排查成本极高。
func New(cfg config.ModelConfig) (LLMProvider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("模型 %q 缺少 model 字段", cfg.Name)
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if name == "" {
		name = config.InferProvider(cfg.Model)
	}
	f, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("暂不支持的 provider %q（已注册: %s）", name, strings.Join(RegisteredProviders(), ", "))
	}
	return f(cfg)
}

// ValidateModel 校验一份模型配置是否可用（含环境变量是否真的取到了值）。
func ValidateModel(cfg config.ModelConfig) error {
	return ValidateConfig(cfg.ToOpenAIConfig())
}
