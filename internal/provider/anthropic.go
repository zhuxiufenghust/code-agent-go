package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/base"
	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

// AnthropicProvider 通过 Anthropic Messages API 调用 Claude 系列模型。
//
// 与 OpenAI 兼容层的关键差异（也是为什么必须单独实现一套，而不是复用 /chat/completions）：
//   - system 是请求体的独立字段，不在 messages 里；
//   - max_tokens 必填，缺失会以 400 直接失败；
//   - 工具调用的结果回灌是 user 消息里的 tool_result 内容块，不是独立 role=tool 消息；
//   - 流式增量按 content_block 切分：同一个块可能是文本、思考或工具入参 JSON 的片段，
//     因此必须按 block index 累加，跨块拼接会得到乱序的工具参数。
type AnthropicProvider struct {
	UsageTracker
	config *config.AnthropicConfig
	client *http.Client
}

const (
	// anthropicVersion 是 Messages API 的版本头；官方要求显式带上。
	anthropicVersion = "2023-06-01"
	// anthropicMessagesPath 是 Messages API 的路径（baseURLEnv 形如 https://api.anthropic.com）。
	anthropicMessagesPath = "/v1/messages"
	// anthropicDefaultTimeout 兜底超时：网关挂住时不能让一轮运行无限等待。
	anthropicDefaultTimeout = 10 * time.Minute
)

func NewAnthropicProvider(cfg *config.AnthropicConfig) *AnthropicProvider {
	if cfg == nil {
		cfg = &config.AnthropicConfig{}
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = config.DefaultAnthropicMaxTokens
	}
	return &AnthropicProvider{
		config: cfg,
		client: &http.Client{Timeout: anthropicDefaultTimeout},
	}
}

// anthropicRequest 是 Messages API 的请求体。
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// anthropicMessage 只用于序列化：Content 可能是字符串（纯文本）或内容块数组。
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
}

// Generate 非流式调用：转调统一的 GenerateFromStream，收集逻辑与所有 Provider 一致。
func (p *AnthropicProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return GenerateFromStream(ctx, p.GenerateStream, msgs, availableTools)
}

func (p *AnthropicProvider) GenerateStream(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	body, err := p.buildRequest(msgs, availableTools, true)
	if err != nil {
		return nil, err
	}
	req, err := p.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, explainAnthropicTransportError(err, p.config)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, decodeAnthropicError(resp)
	}

	ch := make(chan schema.StreamChunk)
	base.GoWrap(func() {
		defer close(ch)
		defer resp.Body.Close()
		p.consumeSSE(ctx, resp.Body, ch)
	})
	return ch, nil
}

// buildRequest 把引擎侧的上下文翻译成 Messages API 的请求体。
func (p *AnthropicProvider) buildRequest(msgs []schema.Message, availableTools []schema.ToolDefinition, stream bool) ([]byte, error) {
	if p.config == nil || strings.TrimSpace(p.config.Model) == "" {
		return nil, fmt.Errorf("anthropic 配置缺少 model")
	}
	system, messages := p.convertMessages(msgs)
	req := anthropicRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		System:    system,
		Messages:  messages,
		Stream:    stream,
	}
	if len(availableTools) > 0 {
		req.Tools = make([]anthropicTool, 0, len(availableTools))
		for _, t := range availableTools {
			req.Tools = append(req.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: nonNilSchema(t.InputSchema),
			})
		}
	}
	// thinking 只有显式要求时才打开：开启后思考 token 会计入输出计费，
	// 而 includeReasoning=false 的语义就是"不要为看不见的推理付费"。
	if p.config.IncludeReasoning {
		req.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: config.DefaultAnthropicThinkingBudget}
	}
	return json.Marshal(req)
}

func (p *AnthropicProvider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	if p.config == nil {
		return nil, fmt.Errorf("anthropic 配置为空")
	}
	if strings.TrimSpace(p.config.ApiKeyEnv) == "" {
		return nil, fmt.Errorf("anthropic.apiKeyEnv 为空（应为存放 API Key 的环境变量名）")
	}
	if strings.TrimSpace(p.config.BaseURLEnv) == "" {
		return nil, fmt.Errorf("anthropic.baseURLEnv 为空（应为存放服务地址的环境变量名）")
	}
	apiKey := os.Getenv(p.config.ApiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("环境变量 %s 未设置或为空", p.config.ApiKeyEnv)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(p.config.BaseURLEnv)), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("环境变量 %s 未设置或为空", p.config.BaseURLEnv)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+anthropicMessagesPath, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	return req, nil
}

// convertMessages 把通用消息序列拆成 system 与 Messages API 的 messages。
//
// 两个必须处理的形态差异：
//  1. system 不在 messages 里，而是顶层字段（多条 system 合并为一段）；
//  2. 工具结果在通用结构里是一条 role=user + ToolCallID 的消息，
//     在 Messages API 里必须是 user 消息中的 tool_result 内容块。
func (p *AnthropicProvider) convertMessages(msgs []schema.Message) (string, []anthropicMessage) {
	var systemParts []string
	out := make([]anthropicMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case schema.SystemRole:
			if strings.TrimSpace(m.Content) != "" {
				systemParts = append(systemParts, m.Content)
			}
		case schema.AssistantRole:
			blocks := make([]any, 0, 1+len(m.ToolCalls))
			if strings.TrimSpace(m.Content) != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: decodeArgs(tc.Arguments),
				})
			}
			if len(blocks) == 0 {
				// Messages API 不接受空 content：用空文本块占位，而不是丢掉这条消息
				// （丢掉会让 assistant 的 tool_use 失去上下文，服务端报 "tool_use without assistant"）。
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: ""})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		default:
			// user 消息：可能是真正的用户输入，也可能是回灌的工具结果。
			if strings.TrimSpace(m.ToolCallID) != "" {
				out = append(out, anthropicMessage{Role: "user", Content: []any{anthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
					IsError:   m.IsError,
				}}})
				continue
			}
			out = append(out, anthropicMessage{Role: "user", Content: m.Content})
		}
	}
	return strings.Join(systemParts, "\n\n"), out
}

func decodeArgs(raw json.RawMessage) map[string]any {
	input := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			// 参数不是合法 JSON 对象时退回空对象：宁可让工具侧报"缺少参数"（可自愈），
			// 也不要让整个请求因为一条畸形参数被服务端 400 拒掉。
			log.Warn("anthropic_tool_args_invalid", zap.String("raw", string(raw)), zap.Error(err))
		}
	}
	return input
}

func nonNilSchema(s any) any {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return s
}

// sseEvent 是 Messages API 的流式事件（只取用到的字段）。
type sseEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Usage anthropicUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// blockState 累积一个 content block 的流式片段。
type blockState struct {
	isTool   bool
	id       string
	name     string
	text     strings.Builder
	jsonPart strings.Builder
	thinking strings.Builder
}

// consumeSSE 解析 SSE 流并把结果写进 chunk channel。
func (p *AnthropicProvider) consumeSSE(ctx context.Context, body io.Reader, ch chan<- schema.StreamChunk) {
	var (
		blocks   = map[int]*blockState{}
		order    []int
		usage    schema.Usage
		sawStop  bool
		finalMsg *schema.Message
	)

	ensureBlock := func(idx int) *blockState {
		if b, ok := blocks[idx]; ok {
			return b
		}
		b := &blockState{}
		blocks[idx] = b
		order = append(order, idx)
		return b
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt sseEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			// 单条畸形事件不应该吃掉整条流：记录下来继续，最终由 message_stop 收敛。
			log.Warn("anthropic_sse_event_invalid", zap.String("payload", truncateForLog(payload)), zap.Error(err))
			continue
		}
		switch evt.Type {
		case "message_start":
			if evt.Message.Usage.InputTokens > 0 {
				usage.InputTokens = evt.Message.Usage.InputTokens
			}
		case "content_block_start":
			b := ensureBlock(evt.Index)
			b.isTool = evt.ContentBlock.Type == "tool_use"
			b.id = evt.ContentBlock.ID
			b.name = evt.ContentBlock.Name
		case "content_block_delta":
			b := ensureBlock(evt.Index)
			switch evt.Delta.Type {
			case "text_delta":
				b.text.WriteString(evt.Delta.Text)
				sendStreamChunk(ctx, ch, schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: evt.Delta.Text})
			case "thinking_delta":
				b.thinking.WriteString(evt.Delta.Thinking)
				sendStreamChunk(ctx, ch, schema.StreamChunk{Type: schema.StreamChunkThinkingDelta, Delta: evt.Delta.Thinking})
			case "input_json_delta":
				b.jsonPart.WriteString(evt.Delta.PartialJSON)
			default:
				if evt.Delta.Text != "" {
					b.text.WriteString(evt.Delta.Text)
					sendStreamChunk(ctx, ch, schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: evt.Delta.Text})
				}
			}
		case "message_delta":
			if evt.Usage.OutputTokens > 0 {
				usage.OutputTokens = evt.Usage.OutputTokens
			}
		case "message_stop":
			sawStop = true
			finalMsg = assembleMessage(blocks, order)
		case "error":
			msg := evt.Error.Message
			if msg == "" {
				msg = "anthropic 流式返回未知错误"
			}
			sendStreamChunk(ctx, ch, schema.StreamChunk{Type: schema.StreamChunkError, Err: fmt.Errorf("%s: %s", evt.Error.Type, msg)})
			return
		}
	}

	if !sawStop {
		// 没收到 message_stop 意味着响应被截断（网络中断/网关提前关闭）。
		// 此时宁可报错也不要回灌一条残缺的 assistant 消息——后者会让后续
		// 工具调用缺少配对，症状比一次失败更难排查。
		sendStreamChunk(ctx, ch, schema.StreamChunk{
			Type: schema.StreamChunkError,
			Err:  fmt.Errorf("anthropic 流式响应未正常结束（缺少 message_stop）"),
		})
		return
	}

	p.AccumulateUsage(&usage)
	sendStreamChunk(ctx, ch, schema.StreamChunk{
		Type:    schema.StreamChunkDone,
		Message: finalMsg,
		Usage:   &usage,
	})
}

// assembleMessage 把各 block 的累积结果拼成一条 assistant 消息。
// 工具调用按 block 出现顺序排列：引擎按 tool_call id 与工具结果配对，顺序不是关键，
// 但保持"模型产出顺序"能让日志与上下文对人类更可读。
func assembleMessage(blocks map[int]*blockState, order []int) *schema.Message {
	msg := &schema.Message{Role: schema.AssistantRole}
	var textParts []string
	for _, idx := range order {
		b := blocks[idx]
		if b == nil {
			continue
		}
		if b.isTool {
			args := strings.TrimSpace(b.jsonPart.String())
			if args == "" {
				args = "{}"
			}
			if !json.Valid([]byte(args)) {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
				ID:        b.id,
				Name:      b.name,
				Arguments: json.RawMessage(args),
			})
			continue
		}
		if b.text.Len() > 0 {
			textParts = append(textParts, b.text.String())
		}
	}
	msg.Content = strings.Join(textParts, "")
	return msg
}

// decodeAnthropicError 把错误响应翻译成可操作的提示。
// 直接把状态码抛给上层，使用者看到 "400 Bad Request" 完全无法行动，
// 而 Messages API 的 body 里就有明确的 type/message。
func decodeAnthropicError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		return fmt.Errorf("anthropic 返回 %d %s: %s", resp.StatusCode, body.Error.Type, body.Error.Message)
	}
	return fmt.Errorf("anthropic 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}

func explainAnthropicTransportError(err error, cfg *config.AnthropicConfig) error {
	if err == nil || cfg == nil {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, "unsupported protocol scheme") && !strings.Contains(msg, "no such host") {
		return err
	}
	name := strings.TrimSpace(cfg.BaseURLEnv)
	if name == "" {
		return fmt.Errorf("请求无法发出：anthropic.baseURLEnv 未配置（原始错误: %v）", err)
	}
	return fmt.Errorf("请求无法发出：环境变量 %s 的当前值 %q 不可达（原始错误: %v）",
		name, strings.TrimSpace(os.Getenv(name)), err)
}

func truncateForLog(s string) string {
	const limit = 512
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...[已截断]"
}
