package config

import (
	"encoding/json"
	"os"
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
	Log    *LogConfig    `json:"log"`    // 日志相关配置
	OpenAI *OpenAIConfig `json:"openai"` // OpenAI 相关配置
}
type LtmConfig struct {
	// Add fields for LTM configuration here
}

type MemoryConfig struct {
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
