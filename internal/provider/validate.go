package provider

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
)

// 为什么需要启动期校验：
// BaseURL 为空不会在构造 client 时报错，而是等到第一次请求时报
// `unsupported protocol scheme ""`，错误信息既不提 baseURL 也不提是哪个环境变量，
// 排查只能靠猜。配置问题必须在"发请求之前"拦下来，且要指明该改哪个变量。
func ValidateConfig(cfg *config.OpenAIConfig) error {
	if cfg == nil {
		return errors.New("配置缺少 openai 段")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("openai.model 为空")
	}
	if strings.TrimSpace(cfg.ApiKeyEnv) == "" {
		return errors.New("openai.apiKeyEnv 为空（应为存放 API Key 的环境变量名）")
	}
	if strings.TrimSpace(cfg.BaseURLEnv) == "" {
		return errors.New("openai.baseURLEnv 为空（应为存放服务地址的环境变量名）")
	}
	if strings.TrimSpace(os.Getenv(cfg.ApiKeyEnv)) == "" {
		return fmt.Errorf("环境变量 %s 未设置或为空：请确认 .env 已加载，且其中的变量名与 openai.apiKeyEnv 一致", cfg.ApiKeyEnv)
	}
	raw := strings.TrimSpace(os.Getenv(cfg.BaseURLEnv))
	if raw == "" {
		return fmt.Errorf("环境变量 %s 未设置或为空：请确认 .env 已加载，且其中的变量名与 openai.baseURLEnv 一致", cfg.BaseURLEnv)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("环境变量 %s 的值 %q 不是合法 URL: %w", cfg.BaseURLEnv, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("环境变量 %s 的值 %q 缺少 http/https 前缀（当前 scheme=%q）", cfg.BaseURLEnv, raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("环境变量 %s 的值 %q 缺少主机名", cfg.BaseURLEnv, raw)
	}
	return nil
}

// explainTransportError 把传输层错误翻译成可操作的提示。
// 使用者看到 `unsupported protocol scheme ""` 时，无法把它和"某个环境变量没配"联系起来，
// 这里补上这层因果。
func explainTransportError(err error, cfg *config.OpenAIConfig) error {
	if err == nil || cfg == nil {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, "unsupported protocol scheme") &&
		!strings.Contains(msg, "no Host in request URL") {
		return err
	}
	name := strings.TrimSpace(cfg.BaseURLEnv)
	if name == "" {
		return errors.New("请求无法发出：服务地址为空，请检查 openai.baseURLEnv 配置")
	}
	resolved := strings.TrimSpace(os.Getenv(name))
	if resolved == "" {
		return fmt.Errorf("请求无法发出：环境变量 %s 解析为空，服务地址缺失（原始错误: %v）", name, err)
	}
	return fmt.Errorf("请求无法发出：环境变量 %s 的当前值 %q 不可达或格式不正确（原始错误: %v）", name, resolved, err)
}
