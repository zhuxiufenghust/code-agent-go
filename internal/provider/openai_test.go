package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		// 与 conf/config.json 的线上配置保持一致：不回传推理内容。
		// 此时请求体会附加 enable_thinking=false，思考模型（如 hy4-preview）
		// 不应再输出 reasoning_content——下方断言验证这一点。
		IncludeReasoning: false,
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
	// includeReasoning=false 时请求体带 enable_thinking=false，若网关仍回吐
	// reasoning_content，说明 thinking 并未真正关闭（白白消耗思考 token）。
	if reasoning.Len() > 0 {
		t.Errorf("includeReasoning=false 时仍收到推理内容（thinking 未关闭）: %q", reasoning.String())
	}
}

// TestGenerateStream_ThinkingMode 验证 IncludeReasoning 开关对请求体的影响：
// 关闭时必须附加 thinking={"type":"disabled"}（关闭 hy4-preview 等思考模型的 thinking），
// 开启时不应出现该字段（交由网关/模型默认行为）。
func TestGenerateStream_ThinkingMode(t *testing.T) {
	run := func(t *testing.T, includeReasoning bool) map[string]any {
		t.Helper()
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &gotBody); err != nil {
				t.Errorf("请求体不是合法 JSON: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		defer srv.Close()

		t.Setenv("TEST_OPENAI_KEY_HOLDER", "test-key")
		t.Setenv("TEST_OPENAI_URL_HOLDER", srv.URL)
		cfg := &config.OpenAIConfig{
			Model:            "hy4-preview",
			ApiKeyEnv:        "TEST_OPENAI_KEY_HOLDER",
			BaseURLEnv:       "TEST_OPENAI_URL_HOLDER",
			IncludeReasoning: includeReasoning,
		}
		provider := NewOpenAIProvider(cfg)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ch, err := provider.GenerateStream(ctx, []schema.Message{
			{Role: schema.UserRole, Content: "ping"},
		}, nil)
		if err != nil {
			t.Fatalf("GenerateStream 返回错误: %v", err)
		}
		for chunk := range ch {
			if chunk.Type == schema.StreamChunkError {
				t.Fatalf("流式返回错误 chunk: %v", chunk.Err)
			}
		}
		if gotBody == nil {
			t.Fatal("未捕获到请求体")
		}
		return gotBody
	}

	t.Run("IncludeReasoning关闭时附加thinking=disabled", func(t *testing.T) {
		body := run(t, false)
		v, ok := body["thinking"]
		if !ok {
			t.Fatal("请求体缺少 thinking 字段，thinking 模式未被关闭")
		}
		m, isMap := v.(map[string]any)
		if !isMap || m["type"] != "disabled" {
			t.Fatalf("期望 thinking.type=disabled, 实际 %v", v)
		}
	})

	t.Run("IncludeReasoning开启时不附加thinking", func(t *testing.T) {
		body := run(t, true)
		if _, ok := body["thinking"]; ok {
			t.Fatal("IncludeReasoning=true 时不应附加 thinking 字段")
		}
	})
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
