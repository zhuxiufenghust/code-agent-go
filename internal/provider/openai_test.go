package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// TestOpenAIProvider_GenerateStream_Live 是集成测试，真正连接大模型进行验证。
// 需设置以下环境变量后才会执行，否则自动跳过（避免无密钥 / CI 环境失败）：
//   - TEST_OPENAI_API_KEY:   API Key（承载到 cfg.ApiKeyEnv）
//   - TEST_OPENAI_BASE_URL:  网关 / Proxy 基础地址（承载到 cfg.BaseURLEnv）
//   - TEST_OPENAI_MODEL:     模型名（默认 gpt-4o-mini）
func TestOpenAIProvider_GenerateStream_Live(t *testing.T) {
	apiKey := os.Getenv("TEST_OPENAI_API_KEY")
	baseURL := os.Getenv("TEST_OPENAI_BASE_URL")
	model := os.Getenv("TEST_OPENAI_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}
	if apiKey == "" || baseURL == "" {
		t.Skip("跳过实时大模型连接测试：未设置 TEST_OPENAI_API_KEY / TEST_OPENAI_BASE_URL")
	}

	// 用临时环境变量名承载密钥，复用 NewOpenAIProvider 的 env 读取逻辑。
	t.Setenv("TEST_OPENAI_KEY_HOLDER", apiKey)
	t.Setenv("TEST_OPENAI_URL_HOLDER", baseURL)

	cfg := &config.OpenAIConfig{
		Model:      model,
		ApiKeyEnv:  "TEST_OPENAI_KEY_HOLDER",
		BaseURLEnv: "TEST_OPENAI_URL_HOLDER",
	}
	provider := NewOpenAIProvider(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	msgs := []schema.Message{
		{Role: schema.SystemRole, Content: "You are a helpful assistant."},
		{Role: schema.UserRole, Content: "Reply with exactly the word: pong"},
	}

	ch, err := provider.GenerateStream(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("GenerateStream 返回错误: %v", err)
	}

	var text strings.Builder
	var reasoning strings.Builder
	var gotDone bool
	for chunk := range ch {
		switch chunk.Type {
		case schema.StreamChunkTextDelta:
			text.WriteString(chunk.Delta)
		case schema.StreamChunkThinkingDelta:
			reasoning.WriteString(chunk.Delta)
		case schema.StreamChunkError:
			t.Fatalf("流式返回错误 chunk: %v", chunk.Err)
		case schema.StreamChunkDone:
			gotDone = true
		}
	}

	if !gotDone {
		t.Fatal("未收到 done chunk，流式响应异常中断")
	}
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("未收到任何文本内容，连接/模型调用可能失败")
	}
	t.Logf("模型回复: %q", strings.TrimSpace(text.String()))
	if reasoning.Len() > 0 {
		t.Logf("推理内容: %q", reasoning.String())
	}
}

func TestToRequestOptions(t *testing.T) {
	opts := ToRequestOptions(config.OpenAIOptions{MaxRetries: 3})
	if len(opts) != 1 {
		t.Fatalf("期望 1 个 option, 实际 %d", len(opts))
	}

	empty := ToRequestOptions(config.OpenAIOptions{})
	if len(empty) != 0 {
		t.Fatalf("未设置 MaxRetries 时期望 0 个 option, 实际 %d", len(empty))
	}
}

func TestConvertMessages(t *testing.T) {
	p := &OpenAIProvider{}
	msgs := []schema.Message{
		{Role: schema.SystemRole, Content: "sys"},
		{Role: schema.UserRole, Content: "hi"},
		{Role: schema.UserRole, Content: "tool result", ToolCallID: "call_1"},
		{Role: schema.AssistantRole, Content: "answer"},
	}
	out := p.convertMessages(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("期望转换 %d 条消息, 实际 %d", len(msgs), len(out))
	}
	if out[0].OfSystem == nil {
		t.Error("第一条应为 system 消息")
	}
	if out[1].OfUser == nil {
		t.Error("第二条应为 user 消息")
	}
	if out[2].OfTool == nil {
		t.Error("第三条应为 tool 消息")
	}
	if out[3].OfAssistant == nil {
		t.Error("第四条应为 assistant 消息")
	}
}

func TestExtractReasoningContent(t *testing.T) {
	withReasoning := `{"choices":[{"delta":{"reasoning_content":"let me think"}}]}`
	if got := extractReasoningContent(withReasoning); got != "let me think" {
		t.Errorf("期望 reasoning_content=let me think, 实际 %q", got)
	}

	withReasoningAlt := `{"choices":[{"delta":{"reasoning":"alternate"}}]}`
	if got := extractReasoningContent(withReasoningAlt); got != "alternate" {
		t.Errorf("期望 reasoning=alternate, 实际 %q", got)
	}

	noReasoning := `{"choices":[{"delta":{"content":"hi"}}]}`
	if got := extractReasoningContent(noReasoning); got != "" {
		t.Errorf("期望空, 实际 %q", got)
	}
}
