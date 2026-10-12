package config

import "testing"

// TestApplyDefaults_TokenAndCompactorWindowSync 验证两个窗口字段最终收敛到同一个值：
// 允许两种写法（token.contextWindow / compactor.contextWindow），但结果必须唯一，
// 否则同一个模型会同时有两个"窗口大小"，压缩阈值和告警阈值各算一套。
func TestApplyDefaults_TokenAndCompactorWindowSync(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		expected int
	}{
		{"都没写→默认窗口", &Config{}, DefaultContextWindow},
		{"只写 token 段", &Config{Token: &TokenConfig{ContextWindow: 12345}}, 12345},
		{"只写 compactor 段（老配置）", &Config{Compactor: &CompactorConfig{ContextWindow: 6789}}, 6789},
		{"两边都写时以 token 段为准", &Config{
			Token:     &TokenConfig{ContextWindow: 100},
			Compactor: &CompactorConfig{ContextWindow: 200},
		}, 100},
	}
	for _, c := range cases {
		c.cfg.ApplyDefaults()
		if c.cfg.Token.ContextWindow != c.expected {
			t.Fatalf("%s: token.contextWindow = %d, 期望 %d", c.name, c.cfg.Token.ContextWindow, c.expected)
		}
		if c.cfg.Compactor.ContextWindow != c.expected {
			t.Fatalf("%s: compactor.contextWindow = %d, 期望 %d", c.name, c.cfg.Compactor.ContextWindow, c.expected)
		}
	}
}

func TestApplyDefaults_TokenWarnRatio(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Token.WarnRatio != DefaultWarnRatio {
		t.Fatalf("缺省水位应为 %v, 实际 %v", DefaultWarnRatio, cfg.Token.WarnRatio)
	}

	// 越界值必须回落：ratio>1 会让告警永不触发，<=0 会让每轮都告警（等于没有水位线）。
	for _, r := range []float64{0, -0.5, 1.5} {
		cfg = &Config{Token: &TokenConfig{WarnRatio: r}}
		cfg.ApplyDefaults()
		if cfg.Token.WarnRatio != DefaultWarnRatio {
			t.Fatalf("非法水位 %v 应回落默认, 实际 %v", r, cfg.Token.WarnRatio)
		}
	}
	cfg = &Config{Token: &TokenConfig{WarnRatio: 0.5}}
	cfg.ApplyDefaults()
	if cfg.Token.WarnRatio != 0.5 {
		t.Fatalf("合法显式值应保留, 实际 %v", cfg.Token.WarnRatio)
	}
}

// TestApplyDefaults_ModelsFromOpenAI 验证没有 models 段时会由 openai 段合成清单：
// 上层因此只需面对"选一个 ModelConfig"这一种形态，不必兼容两套入口。
func TestApplyDefaults_ModelsFromOpenAI(t *testing.T) {
	cfg := &Config{OpenAI: &OpenAIConfig{
		Model:            "gpt-4o",
		BaseURLEnv:       "BASE",
		ApiKeyEnv:        "KEY",
		IncludeReasoning: true,
		Options:          OpenAIOptions{MaxRetries: 7},
	}}
	cfg.ApplyDefaults()

	if len(cfg.Models.Items) != 1 {
		t.Fatalf("应合成 1 条模型配置, 实际 %d", len(cfg.Models.Items))
	}
	item := cfg.Models.Items[0]
	if item.Model != "gpt-4o" || item.BaseURLEnv != "BASE" || item.ApiKeyEnv != "KEY" {
		t.Fatalf("合成结果丢失了 openai 段信息: %+v", item)
	}
	if item.MaxRetries != 7 || !item.IncludeReasoning {
		t.Fatalf("嵌套选项未透传: %+v", item)
	}
	resolved, err := cfg.Models.ResolveModel("")
	if err != nil {
		t.Fatalf("缺省模型应可解析: %v", err)
	}
	if resolved.Name != item.Name {
		t.Fatalf("缺省应取列表首项, 实际 %v", resolved.Name)
	}
}

func TestModelsResolve(t *testing.T) {
	cfg := &Config{Models: &ModelsConfig{Default: "fast", Items: []ModelConfig{
		{Name: "fast", Model: "m1", Provider: ProviderOpenAI},
		{Name: "smart", Model: "m2", Provider: ProviderOpenAI},
	}}}
	cfg.ApplyDefaults()

	if got, err := cfg.Models.ResolveModel("smart"); err != nil || got.Model != "m2" {
		t.Fatalf("按名解析失败: got=%+v err=%v", got, err)
	}
	if got, err := cfg.Models.ResolveModel(""); err != nil || got.Name != "fast" {
		t.Fatalf("空名字应取 default: got=%+v err=%v", got, err)
	}
	if _, err := cfg.Models.ResolveModel("nope"); err == nil {
		t.Fatal("未知模型名应报错而不是静默回落")
	}
	if _, err := (&ModelsConfig{}).ResolveModel(""); err == nil {
		t.Fatal("空清单应报错")
	}
}

func TestApplyDefaults_SandboxAndObservability(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Sandbox.Enabled {
		t.Fatal("沙箱默认必须关闭：开启会改变 bash 的执行载体，属于显式选择")
	}
	if cfg.Sandbox.Image != DefaultSandboxImage {
		t.Fatalf("缺省镜像应为 %q, 实际 %q", DefaultSandboxImage, cfg.Sandbox.Image)
	}
	if cfg.Observability.Enabled {
		t.Fatal("观测导出默认关闭：在没有采集侧的环境里灌 JSON 会淹没错误日志")
	}
	if cfg.Observability.Exporter != ExporterStderr {
		t.Fatalf("缺省导出目标应为 stderr（stdout 会被 TUI 界面占用）, 实际 %q", cfg.Observability.Exporter)
	}
	if cfg.Observability.DumpIntervalSecs != DefaultDumpIntervalSecs {
		t.Fatalf("缺省导出周期应为 %d, 实际 %d", DefaultDumpIntervalSecs, cfg.Observability.DumpIntervalSecs)
	}
	if cfg.LTM.Enabled {
		t.Fatal("长期记忆默认关闭：写操作工具需要显式授权")
	}
	if cfg.LTM.MaxResults != DefaultLtmMaxResults {
		t.Fatalf("缺省召回上限应为 %d, 实际 %d", DefaultLtmMaxResults, cfg.LTM.MaxResults)
	}
}

// TestMemoryPersistenceEnabled 验证"未配置"与"显式关闭"能被区分开：
// 这是 MemoryConfig.Persistence 用 *bool 而不是 bool 的唯一理由。
func TestMemoryPersistenceEnabled(t *testing.T) {
	var nilCfg *MemoryConfig
	if !nilCfg.PersistenceEnabled() {
		t.Fatal("nil 表示未配置，应当默认开启持久化")
	}
	yes, no := true, false
	if !(&MemoryConfig{Persistence: &yes}).PersistenceEnabled() {
		t.Fatal("显式开启应生效")
	}
	if (&MemoryConfig{Persistence: &no}).PersistenceEnabled() {
		t.Fatal("显式关闭应生效")
	}
}

func TestInferProvider(t *testing.T) {
	cases := map[string]string{
		"claude-3-5-sonnet": ProviderAnthropic,
		"anthropic-x":       ProviderAnthropic,
		"ollama-qwen":       ProviderOllama,
		"gpt-4o":            ProviderOpenAI,
		"":                  ProviderOpenAI,
	}
	for model, want := range cases {
		if got := InferProvider(model); got != want {
			t.Fatalf("InferProvider(%q) = %q, 期望 %q", model, got, want)
		}
	}
}
