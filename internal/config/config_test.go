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
