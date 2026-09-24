package context_mng

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

type Summarizer interface {
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)
}

const (
	// summaryMarker 用于标识摘要消息，支持在下次压缩时识别并增量更新。
	summaryMarker = "[Conversation Summary]"

	// defaultSummaryTimeout 是单次摘要请求的默认超时。
	defaultSummaryTimeout = 30 * time.Second

	summarySystemPrompt = `You are a conversation summarizer. Produce a concise structured summary that preserves essential context for continuing the conversation. Output only the summary — no preamble, no explanation.`

	// summaryTemplate 用于首次摘要请求。
	summaryTemplate = "Summarize the following conversation into this structure:\n\n" +
		"**Goal:** What the user is trying to accomplish.\n" +
		"**Progress:** Key actions taken and their results.\n" +
		"**Key Decisions:** Important choices and rationale.\n" +
		"**Next Steps:** What was planned or pending when this segment ends.\n" +
		"**Critical Context:** Facts, file paths, variable names, or constraints the agent must remember.\n\n" +
		"Conversation:\n%s"

	// incrementalTemplate 用于在已有摘要的基础上进行增量更新。
	incrementalTemplate = "Update the existing summary by merging in new conversation content. " +
		"Output the merged summary in the same structure — no preamble.\n\n" +
		"<previous-summary>\n%s\n</previous-summary>\n\n" +
		"New conversation to merge:\n%s"
)

type SummarizationCompactor struct {
	Provider        Summarizer
	MaxTokens       int
	ContextWindow   int
	MinTailMessages int
	// SummaryTimeout 限制单次摘要请求；<=0 时使用 defaultSummaryTimeout。
	SummaryTimeout time.Duration
	// Fallback 在 Provider 调用失败、或摘要后仍超预算时使用。
	// 若为 nil，则创建同配置的 TokenBudgetCompactor。
	Fallback Compactor
}

func NewSummarizationCompactor(provider Summarizer, contextWindow int, minTailMessages int,
	fallback Compactor) *SummarizationCompactor {
	maxTokens := contextWindow * 80 / 100
	if minTailMessages <= 0 {
		minTailMessages = 6
	}
	return &SummarizationCompactor{
		Provider:        provider,
		MaxTokens:       maxTokens,
		ContextWindow:   contextWindow,
		MinTailMessages: minTailMessages,
		SummaryTimeout:  defaultSummaryTimeout,
		Fallback:        fallback,
	}
}

func (c *SummarizationCompactor) maxTokens() int {
	if c.MaxTokens <= 0 {
		return 160_000
	}
	return c.MaxTokens
}

func (c *SummarizationCompactor) minTail() int {
	if c.MinTailMessages <= 0 {
		return 6
	}
	return c.MinTailMessages
}

func (c *SummarizationCompactor) summaryTimeout() time.Duration {
	if c.SummaryTimeout <= 0 {
		return defaultSummaryTimeout
	}
	return c.SummaryTimeout
}

func (c *SummarizationCompactor) fallback() Compactor {
	if c.Fallback != nil {
		return c.Fallback
	}
	return NewTokenBudgetCompactor(c.ContextWindow)
}

func (c *SummarizationCompactor) Compact(ctx context.Context, msgs []schema.Message) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return msgs, nil
	}
	if EstimateTokens(msgs) <= c.maxTokens() {
		return msgs, nil
	}

	system, rest := splitSystem(msgs)
	if len(rest) <= c.minTail() {
		// 对话段已无法再切分出可摘要的头部，压缩无从下手。
		return msgs, nil
	}

	// 只摘要 system 之外的对话历史，避免把 system prompt 喂进摘要请求。
	head := rest[:len(rest)-c.minTail()]
	tail := rest[len(rest)-c.minTail():]

	summary, err := c.summarize(ctx, head)
	if err != nil {
		return c.fallback().Compact(ctx, msgs)
	}

	result := c.buildCompactedResult(system, summary, tail)
	// 摘要本身可能过长，导致压缩后仍超预算：此时交由 fallback 兜底。
	if EstimateTokens(result) > c.maxTokens() {
		return c.fallback().Compact(ctx, msgs)
	}
	return result, nil
}

// summarize 把 head 段（不含 system）交给 Provider 生成摘要。
// 若 head 中已存在上一轮的摘要消息，则走增量合并模板。
func (c *SummarizationCompactor) summarize(ctx context.Context, msgs []schema.Message) (string, error) {
	if c.Provider == nil {
		return "", errors.New("SummarizationCompactor: Provider is nil")
	}

	var prevSummary string
	var lines []string
	for _, msg := range msgs {
		if msg.Role == schema.SystemRole {
			continue
		}
		// 上一轮摘要：取出内容作为增量合并的基线。
		if strings.Contains(msg.Content, summaryMarker) {
			prevSummary = strings.TrimPrefix(msg.Content, summaryMarker+"\n")
			continue
		}
		if msg.ToolCallID != "" {
			if msg.Content == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("[tool_result %s]: %s", msg.ToolCallID, msg.Content))
			continue
		}
		// 文本内容
		if msg.Content != "" {
			lines = append(lines, fmt.Sprintf("[%s]: %s", msg.Role, msg.Content))
		}
		// 工具调用请求
		for _, tc := range msg.ToolCalls {
			lines = append(lines, fmt.Sprintf("[tool_call %s(%s)]: %s", tc.Name, tc.ID, string(tc.Arguments)))
		}
	}
	if prevSummary == "" && len(lines) == 0 {
		return "", errors.New("SummarizationCompactor: nothing to summarize")
	}

	conversationText := strings.Join(lines, "\n")
	var userContent string
	if prevSummary != "" {
		userContent = fmt.Sprintf(incrementalTemplate, prevSummary, conversationText)
	} else {
		userContent = fmt.Sprintf(summaryTemplate, conversationText)
	}
	sysMsg := schema.Message{Role: schema.SystemRole, Content: summarySystemPrompt}
	userMsg := schema.Message{
		Role:    schema.UserRole,
		Content: userContent,
	}

	summaryCtx, cancel := context.WithTimeout(ctx, c.summaryTimeout())
	defer cancel()

	resp, usage, err := c.Provider.Generate(summaryCtx, []schema.Message{sysMsg, userMsg}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("SummarizationCompactor: empty response from provider")
	}
	// 摘要调用自身也有成本：provider 级用量（AccumulateUsage）会计入它，
	// 但会话级用量只统计主循环调用，两边口径因此天然不同——这里至少让它可观测。
	if usage != nil {
		log.Debug("summary_usage", zap.Int("input_tokens", usage.InputTokens),
			zap.Int("output_tokens", usage.OutputTokens))
	}

	return resp.Content, nil
}

func (c *SummarizationCompactor) buildCompactedResult(system []schema.Message, summary string, tail []schema.Message) []schema.Message {
	summaryContent := summaryMarker + "\n" + summary
	summaryMsg := schema.Message{
		Role:    schema.UserRole,
		Content: summaryContent,
	}
	result := make([]schema.Message, 0, len(system)+1+len(tail))
	result = append(result, system...) // system 始终保留
	result = append(result, summaryMsg)
	result = append(result, tail...) // 最近消息原样保留
	return repairOrphanedToolPairs(result)
}
