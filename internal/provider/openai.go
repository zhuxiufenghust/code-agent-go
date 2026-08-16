package provider

import (
	"context"
	"os"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/tidwall/gjson"
	"github.com/zhuxiufenghust/code-agent-go/internal/base"
	"github.com/zhuxiufenghust/code-agent-go/internal/config"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// LLMProvider 定义了与大语言模型（LLM）交互的接口。它提供了两种主要的调用方式：
// 1. Generate: 一次性发送完整的对话上下文，获取模型的最终响应。
// 2. GenerateStream: 以流式方式发送对话上下文，逐步接收模型的增量输出。
// 相关的apiKey、baseURL等配置不应该直接传递，而应该通过env获取
type OpenAIProvider struct {
	BaseProvider
	config *config.OpenAIConfig

	client openai.Client
}

type ProviderOption func(*OpenAIProvider)

func ToRequestOptions(opts config.OpenAIOptions) []option.RequestOption {
	requestOptions := make([]option.RequestOption, 0)
	if opts.MaxRetries > 0 {
		requestOptions = append(requestOptions, option.WithMaxRetries(opts.MaxRetries))
	}
	return requestOptions
}

func NewOpenAIProvider(cfg *config.OpenAIConfig) *OpenAIProvider {
	apiKey := os.Getenv(cfg.ApiKeyEnv)
	apiBaseUrl := os.Getenv(cfg.BaseURLEnv)

	opts := ToRequestOptions(cfg.Options)
	opts = append(opts, option.WithAPIKey(apiKey), option.WithBaseURL(apiBaseUrl))

	provider := &OpenAIProvider{
		client: openai.NewClient(
			opts...,
		),
		config: cfg,
	}

	return provider
}

func (p *OpenAIProvider) GenerateStream(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	ch := make(chan schema.StreamChunk)
	openaiMsgs := p.convertMessages(msgs)

	reqParams := openai.ChatCompletionNewParams{
		Model:    p.config.Model,
		Messages: openaiMsgs,
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, reqParams)

	base.GoWrap(func() {
		defer close(ch)
		defer stream.Close()
		var contentBuf strings.Builder
		toolAccs := newToolCallAccumulators()
		var actualUsage *schema.Usage

		for stream.Next() {
			chunk := stream.Current()

			// 末尾 Usage chunk：Choices 为空，但 Usage 已填充（当 IncludeUsage=true 时）。
			if chunk.Usage.PromptTokens > 0 {
				actualUsage = &schema.Usage{
					InputTokens:  int(chunk.Usage.PromptTokens),
					OutputTokens: int(chunk.Usage.CompletionTokens),
				}
			}

			if len(chunk.Choices) == 0 {
				continue
			}
			// 提取 reasoning_content（DeepSeek-R1 等模型通过此字段暴露推理内容）。
			if rc := extractReasoningContent(chunk.RawJSON()); rc != "" {
				if !sendStreamChunk(ctx, ch, schema.StreamChunk{
					Type:  schema.StreamChunkThinkingDelta,
					Delta: rc,
				}) {
					return
				}
			}

			delta := chunk.Choices[0].Delta
			if delta.Content != "" {
				contentBuf.WriteString(delta.Content)
				if !sendStreamChunk(ctx, ch, schema.StreamChunk{
					Type:  schema.StreamChunkTextDelta,
					Delta: delta.Content,
				}) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
				idx := int(tc.Index)
				if tc.ID != "" {
					toolAccs.start(idx, tc.ID, tc.Function.Name)
				}
				if tc.Function.Arguments != "" {
					toolAccs.appendArgs(idx, tc.Function.Arguments)
				}
			}
		}

		if err := stream.Err(); err != nil {
			sendStreamChunk(ctx, ch, schema.StreamChunk{
				Type: schema.StreamChunkError,
				Err:  err,
			})
			return
		}

		msg := &schema.Message{
			Role:      schema.AssistantRole,
			Content:   contentBuf.String(),
			ToolCalls: toolAccs.finalize(),
		}

		// Done chunk：使用 select 避免 context 取消时阻塞。
		select {
		case <-ctx.Done():
		case ch <- schema.StreamChunk{
			Type:    schema.StreamChunkDone,
			Message: msg,
			Usage:   actualUsage,
		}:
		}
	})

	return ch, nil
}

func extractReasoningContent(rawJSON string) string {
	if rc := gjson.Get(rawJSON, "choices.0.delta.reasoning_content").String(); rc != "" {
		return rc
	}
	return gjson.Get(rawJSON, "choices.0.delta.reasoning").String()
}
func (p *OpenAIProvider) convertMessages(msgs []schema.Message) []openai.ChatCompletionMessageParamUnion {
	openaiMsgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case schema.SystemRole:
			openaiMsgs = append(openaiMsgs, openai.SystemMessage(msg.Content))
		case schema.UserRole:
			if msg.ToolCallID != "" {
				openaiMsgs = append(openaiMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			} else {
				openaiMsgs = append(openaiMsgs, openai.UserMessage(msg.Content))
			}
		case schema.AssistantRole:
			astParam := openai.ChatCompletionAssistantMessageParam{}
			// 大模型上轮回复回传
			if msg.Content != "" {
				astParam.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}
			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ChatCompletionMessageToolCallUnionParam
				for _, tc := range msg.ToolCalls {
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(tc.Arguments),
							},
						},
					})
				}
				astParam.ToolCalls = toolCalls
			}
			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &astParam,
			})
		default:
			// Handle unknown role if necessary
		}
	}
	return openaiMsgs
}
