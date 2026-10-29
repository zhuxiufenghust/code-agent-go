package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// startAnthropicServer 起一个假 Messages API：记录请求体，并按脚本回放 SSE。
// 断言同时落在"我们发出去的请求"与"我们解析出来的增量"两端，
// 只看一端都证明不了协议转换是对的。
func startAnthropicServer(t *testing.T, sse string) *[]string {
	t.Helper()
	var captured []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		captured = append(captured, string(buf))
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			http.Error(w, "missing version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL_TEST", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY_TEST", "test-key")
	return &captured
}

func newTestAnthropic(t *testing.T) *AnthropicProvider {
	t.Helper()
	return NewAnthropicProvider(&config.AnthropicConfig{
		Model:      "claude-3-5-sonnet-test",
		BaseURLEnv: "ANTHROPIC_BASE_URL_TEST",
		ApiKeyEnv:  "ANTHROPIC_API_KEY_TEST",
	})
}

func TestAnthropicProvider_TextAndUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"，世界"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")
	_ = startAnthropicServer(t, sse)
	p := newTestAnthropic(t)

	msg, usage, err := p.Generate(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if msg == nil || msg.Content != "你好，世界" {
		t.Fatalf("文本增量未按序拼接: %+v", msg)
	}
	if usage == nil || usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("用量未正确累计: %+v", usage)
	}
}

func TestAnthropicProvider_ToolUseAndRequestShape(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":20}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash_tool"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"com"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"mand\":\"ls\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")
	captured := startAnthropicServer(t, sse)
	p := newTestAnthropic(t)

	tools := []schema.ToolDefinition{{Name: "bash_tool", Description: "run cmd", InputSchema: map[string]any{"type": "object"}}}
	msg, _, err := p.Generate(context.Background(), []schema.Message{
		{Role: schema.SystemRole, Content: "you are a helper"},
		{Role: schema.AssistantRole, Content: "", ToolCalls: []schema.ToolCall{{ID: "toolu_0", Name: "bash_tool", Arguments: json.RawMessage(`{"command":"pwd"}`)}}},
		{Role: schema.UserRole, Content: "/app", ToolCallID: "toolu_0"},
		{Role: schema.UserRole, Content: "ls"},
	}, tools)
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if msg == nil || len(msg.ToolCalls) != 1 {
		t.Fatalf("工具调用未被还原: %+v", msg)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "bash_tool" || strings.TrimSpace(string(tc.Arguments)) != `{"command":"ls"}` {
		t.Fatalf("工具调用字段不符: id=%s name=%s args=%s", tc.ID, tc.Name, tc.Arguments)
	}

	var body struct {
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name        string `json:"name"`
			InputSchema any    `json:"input_schema"`
		} `json:"tools"`
		Stream bool `json:"stream"`
	}
	if len(*captured) == 0 {
		t.Fatal("没有捕获到请求体")
	}
	if err := json.Unmarshal([]byte((*captured)[0]), &body); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v (%s)", err, (*captured)[0])
	}
	if body.System != "you are a helper" {
		t.Fatalf("system 未拆为独立字段: %q", body.System)
	}
	if body.MaxTokens <= 0 {
		t.Fatalf("max_tokens 必填, 实际 %d", body.MaxTokens)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "bash_tool" {
		t.Fatalf("工具未转成 Anthropic 形态: %+v", body.Tools)
	}
	raw := (*captured)[0]
	if !strings.Contains(raw, `"type":"tool_use"`) || !strings.Contains(raw, `"type":"tool_result"`) {
		t.Fatalf("历史里的工具调用/结果未转成内容块: %s", raw)
	}
}

func TestAnthropicProvider_ThinkingDeltaForwarded(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"先看看"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"答案"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")
	_ = startAnthropicServer(t, sse)
	p := newTestAnthropic(t)

	ch, err := p.GenerateStream(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	var thinking, text string
	for c := range ch {
		switch c.Type {
		case schema.StreamChunkThinkingDelta:
			thinking += c.Delta
		case schema.StreamChunkTextDelta:
			text += c.Delta
		}
	}
	if thinking != "先看看" {
		t.Fatalf("推理增量未被下发: %q", thinking)
	}
	if text != "答案" {
		t.Fatalf("正文增量不符: %q", text)
	}
}

func TestAnthropicProvider_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens is required"}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_BASE_URL_TEST", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY_TEST", "test-key")
	p := newTestAnthropic(t)

	_, _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "max_tokens is required") {
		t.Fatalf("报错应带上服务端给出的原因, 实际: %v", err)
	}
}

func TestAnthropicProvider_TruncatedStreamErrors(t *testing.T) {
	sse := `data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"半句"}}
`
	_ = startAnthropicServer(t, sse)
	p := newTestAnthropic(t)
	if _, _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "x"}}, nil); err == nil ||
		!strings.Contains(err.Error(), "message_stop") {
		t.Fatalf("流被截断必须报错而不是回灌残缺消息, 实际: %v", err)
	}
}

func TestAnthropicProvider_MissingAPIKeyEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL_TEST", "https://example.invalid")
	_ = os.Unsetenv("ANTHROPIC_API_KEY_TEST")
	p := newTestAnthropic(t)
	_, _, err := p.Generate(context.Background(), []schema.Message{{Role: schema.UserRole, Content: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY_TEST") {
		t.Fatalf("报错应指明是哪个环境变量, 实际: %v", err)
	}
}
