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
// 推断结果落在已注册表里（anthropic 尚未实现，因此会报错而不是悄悄当 openai 发请求）。
func TestNewInfersProviderFromModel(t *testing.T) {
	if _, err := New(config.ModelConfig{Model: "ollama-llama3"}); err != nil {
		t.Fatalf("前缀推断失败: %v", err)
	}
	_, err := New(config.ModelConfig{Model: "claude-3-5-sonnet"})
	if err == nil {
		t.Fatal("未实现的 provider 必须报错，不能静默回落到 OpenAI 兼容实现")
	}
	if !strings.Contains(err.Error(), "暂不支持的 provider") {
		t.Fatalf("错误信息应指出 provider 不受支持, 实际 %v", err)
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
