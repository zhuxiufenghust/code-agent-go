package config

import "testing"

// TestApplyDefaults_MaxLoopTurns 验证循环上限有生产默认值：
// 配置缺 engine 段或缺 maxLoopTurns 时都不能等于"无限制"。
func TestApplyDefaults_MaxLoopTurns(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Engine == nil {
		t.Fatal("engine 段缺失时应补空结构，避免调用方 nil 解引用")
	}
	if cfg.Engine.MaxLoopTurns != DefaultMaxLoopTurns {
		t.Fatalf("期望默认轮次上限 %d, 实际 %d", DefaultMaxLoopTurns, cfg.Engine.MaxLoopTurns)
	}

	// 显式配置优先
	cfg = &Config{Engine: &EngineConfig{MaxLoopTurns: 5}}
	cfg.ApplyDefaults()
	if cfg.Engine.MaxLoopTurns != 5 {
		t.Fatalf("显式配置应被保留, 实际 %d", cfg.Engine.MaxLoopTurns)
	}

	// 0/负值视为未配置，回落到默认值
	cfg = &Config{Engine: &EngineConfig{MaxLoopTurns: -1}}
	cfg.ApplyDefaults()
	if cfg.Engine.MaxLoopTurns != DefaultMaxLoopTurns {
		t.Fatalf("非法值应回落到默认 %d, 实际 %d", DefaultMaxLoopTurns, cfg.Engine.MaxLoopTurns)
	}
}

// TestApplyDefaults_Compactor 验证压缩配置缺段/非法值时回落为生产默认值，
// 避免"配置里没写 compactor"等价于"关闭上下文压缩"。
func TestApplyDefaults_Compactor(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	if cfg.Compactor == nil {
		t.Fatal("compactor 段缺失时应补默认结构，否则调用方拿不到压缩器")
	}
	if cfg.Compactor.Strategy != CompactorStrategySummarization {
		t.Fatalf("默认策略应为 %q, 实际 %q", CompactorStrategySummarization, cfg.Compactor.Strategy)
	}
	if cfg.Compactor.ContextWindow != DefaultContextWindow {
		t.Fatalf("默认上下文窗口应为 %d, 实际 %d", DefaultContextWindow, cfg.Compactor.ContextWindow)
	}
	if cfg.Compactor.MinTail != DefaultMinTail {
		t.Fatalf("默认保留条数应为 %d, 实际 %d", DefaultMinTail, cfg.Compactor.MinTail)
	}

	// 显式配置优先
	cfg = &Config{Compactor: &CompactorConfig{Strategy: CompactorStrategyToken, ContextWindow: 1000, MinTail: 2}}
	cfg.ApplyDefaults()
	if cfg.Compactor.Strategy != CompactorStrategyToken || cfg.Compactor.ContextWindow != 1000 || cfg.Compactor.MinTail != 2 {
		t.Fatalf("显式配置应被保留, 实际 %+v", cfg.Compactor)
	}

	// 0/负值视为未配置，回落到默认值
	cfg = &Config{Compactor: &CompactorConfig{ContextWindow: -1, MinTail: 0}}
	cfg.ApplyDefaults()
	if cfg.Compactor.ContextWindow != DefaultContextWindow || cfg.Compactor.MinTail != DefaultMinTail {
		t.Fatalf("非法值应回落默认, 实际 %+v", cfg.Compactor)
	}
}
