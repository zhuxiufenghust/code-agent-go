package provider

import (
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
)

// TestNewRoutesByExplicitProvider 验证显式 provider 字段优先于名字推断。
func TestNewRoutesByExplicitProvider(t *testing.T) {
	if _, err := New(config.ModelConfig{Name: "m", Model: "gpt-4o-mini", Provider: config.ProviderOpenAI}); err != nil {
		t.Fatalf("openai 路由失败: %v", err)
	}
	if _, err := New(config.ModelConfig{Name: "local", Model: "qwen2.5", Provider: config.ProviderOllama}); err != nil {
		t.Fatalf("ollama 应当复用 OpenAI 兼容端点: %v", err)
	}
}

// TestNewInfersProviderFromModel 验证未写 provider 时按模型前缀推断，
// 推断结果必须落在已注册表里：claude-/anthropic- 前缀走 Anthropic Messages API，
// 未注册的 provider 必须报错而不是静默回落到 OpenAI 兼容实现。
func TestNewInfersProviderFromModel(t *testing.T) {
	if _, err := New(config.ModelConfig{Model: "ollama-llama3"}); err != nil {
		t.Fatalf("前缀推断失败: %v", err)
	}
	pr, err := New(config.ModelConfig{Model: "claude-3-5-sonnet", BaseURLEnv: "ANTHROPIC_BASE_URL_TEST", ApiKeyEnv: "ANTHROPIC_API_KEY_TEST"})
	if err != nil {
		t.Fatalf("claude 前缀应路由到已实现的 anthropic: %v", err)
	}
	if _, ok := pr.(*AnthropicProvider); !ok {
		t.Fatalf("claude 前缀应构造 AnthropicProvider, 实际 %T", pr)
	}
	_, err = New(config.ModelConfig{Provider: "not-exist", Model: "whatever"})
	if err == nil || !strings.Contains(err.Error(), "暂不支持的 provider") {
		t.Fatalf("未注册的 provider 必须报错并列出已注册项, 实际 %v", err)
	}
}

func TestNewRejectsMissingModel(t *testing.T) {
	if _, err := New(config.ModelConfig{Name: "empty"}); err == nil {
		t.Fatal("缺少 model 字段应报错")
	}
}

func TestRegisterOverrides(t *testing.T) {
	name := "unit-test-provider"
	defer func() {
		factoryMu.Lock()
		delete(factories, name)
		factoryMu.Unlock()
	}()

	factoryMu.Lock()
	delete(factories, name)
	factoryMu.Unlock()

	Register(name, func(cfg config.ModelConfig) (LLMProvider, error) { return nil, nil })
	if _, ok := Lookup(name); !ok {
		t.Fatal("注册后应可查到")
	}
	if len(RegisteredProviders()) == 0 {
		t.Fatal("RegisteredProviders 不应为空")
	}
}
