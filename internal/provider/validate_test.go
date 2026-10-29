package provider

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
)

// setEnvVar 在测试内设置一个临时环境变量并在结束后清理。
func setEnvVar(t *testing.T, name, value string) {
	t.Helper()
	t.Setenv(name, value)
}

func TestValidateConfig_Valid(t *testing.T) {
	setEnvVar(t, "T_VALID_KEY", "sk-test")
	setEnvVar(t, "T_VALID_URL", "https://api.example.com/v1")

	err := ValidateConfig(&config.OpenAIConfig{
		Model:      "m1",
		ApiKeyEnv:  "T_VALID_KEY",
		BaseURLEnv: "T_VALID_URL",
	})
	if err != nil {
		t.Errorf("合法配置不应报错, 实际: %v", err)
	}
}

func TestValidateConfig_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.OpenAIConfig
		want string
	}{
		{"nil 配置", nil, "openai 段"},
		{"缺 model", &config.OpenAIConfig{ApiKeyEnv: "K", BaseURLEnv: "U"}, "openai.model"},
		{"缺 apiKeyEnv", &config.OpenAIConfig{Model: "m", BaseURLEnv: "U"}, "openai.apiKeyEnv"},
		{"缺 baseURLEnv", &config.OpenAIConfig{Model: "m", ApiKeyEnv: "K"}, "openai.baseURLEnv"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateConfig(c.cfg)
			if err == nil {
				t.Fatal("期望返回错误")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应包含 %q, 实际: %v", c.want, err)
			}
		})
	}
}

// TestValidateConfig_EnvNotSet 覆盖本次线上问题的场景：
// 配置里写了环境变量名，但变量没有值（.env 没加载），必须在此之前就报出来。
func TestValidateConfig_EnvNotSet(t *testing.T) {
	// 确保这两个变量不存在
	os.Unsetenv("T_MISSING_KEY")
	os.Unsetenv("T_MISSING_URL")

	err := ValidateConfig(&config.OpenAIConfig{
		Model:      "m",
		ApiKeyEnv:  "T_MISSING_KEY",
		BaseURLEnv: "T_MISSING_URL",
	})
	if err == nil {
		t.Fatal("环境变量为空时应返回错误")
	}
	if !strings.Contains(err.Error(), "T_MISSING_URL") && !strings.Contains(err.Error(), "T_MISSING_KEY") {
		t.Errorf("错误信息应指出具体缺少哪个环境变量, 实际: %v", err)
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("错误信息应提示检查 .env 是否加载, 实际: %v", err)
	}
}

func TestValidateConfig_BadBaseURL(t *testing.T) {
	setEnvVar(t, "T_KEY", "sk-test")

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"无 scheme", "api.example.com/v1", "缺少 http/https 前缀"},
		{"只有 scheme 分隔符", "://api.example.com", "不是合法 URL"},
		{"缺主机名", "https:///v1", "缺少主机名"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("T_URL", c.value)
			err := ValidateConfig(&config.OpenAIConfig{Model: "m", ApiKeyEnv: "T_KEY", BaseURLEnv: "T_URL"})
			if err == nil {
				t.Fatal("期望返回错误")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应包含 %q, 实际: %v", c.want, err)
			}
		})
	}
}

// TestExplainTransportError 验证传输层错误被翻译成"该改哪个环境变量"级别的信息。
func TestExplainTransportError(t *testing.T) {
	cfg := &config.OpenAIConfig{ApiKeyEnv: "T_KEY", BaseURLEnv: "T_URL_EMPTY"}
	os.Unsetenv("T_URL_EMPTY")

	// target：openai-go 在 BaseURL 为空时的真实报错形态。
	err := errors.New(`Post "/chat/completions": unsupported protocol scheme ""`)
	got := explainTransportError(err, cfg)
	if !strings.Contains(got.Error(), "T_URL_EMPTY") {
		t.Errorf("应指出环境变量 T_URL_EMPTY, 实际: %v", got)
	}

	// 非目标错误原样返回，不做多余包装。
	plain := errors.New("context deadline exceeded")
	if got := explainTransportError(plain, cfg); got != plain {
		t.Errorf("无关错误应原样返回, 实际: %v", got)
	}
	if explainTransportError(nil, cfg) != nil {
		t.Error("nil 错误应返回 nil")
	}
}
