package context_mng

import (
	"context"
	"unicode/utf8"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type Compactor interface {
	Compact(ctx context.Context, msg []schema.Message) ([]schema.Message, error)
}

const (
	charsPerToken = 4

	// tokensPerMessage 覆盖 role、消息分隔等单条消息的固定开销。
	tokensPerMessage = 4

	// placeholderToolResult 是为缺失响应的 tool_call 补齐的占位输出内容。
	placeholderToolResult = "[工具结果不可用：上下文已被压缩]"
)

// messageChars 返回单条消息的估算字符数。
// 使用 rune 而非字节计数：UTF-8 下中文占 3 字节，按字节折算会严重低估中文 token。
func messageChars(m schema.Message) int {
	total := utf8.RuneCountInString(m.Content)
	for _, tc := range m.ToolCalls {
		total += utf8.RuneCountInString(tc.ID) + utf8.RuneCountInString(tc.Name) + len(tc.Arguments)
	}
	return total + utf8.RuneCountInString(m.ToolCallID)
}

func estimateChars(msgs []schema.Message) int {
	total := 0
	for _, m := range msgs {
		total += messageChars(m)
	}
	return total
}

// tokensFromChars 把字符数与消息条数折算为 token 数，保证与 EstimateTokens 口径一致。
// 压缩循环需要增量扣减 token，因此以“字符数”为中间量避免整数除法的累计误差。
func tokensFromChars(chars, msgCount int) int {
	if msgCount == 0 {
		return 0
	}
	return chars/charsPerToken + msgCount*tokensPerMessage
}

func EstimateTokens(msgs []schema.Message) int {
	return tokensFromChars(estimateChars(msgs), len(msgs))
}

// splitSystem 把消息切分为前导 system 段与后续对话段。
// 前导 system 可能不存在（例如 system 由 provider 侧注入），此时压缩照常作用于对话段。
func splitSystem(msgs []schema.Message) (system []schema.Message, rest []schema.Message) {
	i := 0
	for i < len(msgs) && msgs[i].Role == schema.SystemRole {
		i++
	}
	return msgs[:i], msgs[i:]
}

// joinMessages 按顺序拼接若干消息段，返回新切片（不复用入参底层数组）。
func joinMessages(parts ...[]schema.Message) []schema.Message {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]schema.Message, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// repairOrphanedToolPairs 在压缩后执行双向工具对完整性修复（HermesAgent 模式）：
//  1. 删除无对应 tool_call 的孤立 user tool_result 消息
//  2. 删除同一 tool_call 的重复 tool_result（只保留第一条）
//  3. 为缺少响应的 assistant tool_call 插入占位 user tool_result
//
// Anthropic Messages API 要求 tool_call 与 tool_result 必须成对出现，违反此约束会导致 API 400 错误。
// 占位消息会排在该 assistant 之后已存在的 tool_result 之后，避免打乱 tool_result 的原始顺序。
func repairOrphanedToolPairs(msgs []schema.Message) []schema.Message {
	// 收集所有 assistant 发起的 tool_call ID 集合。
	calledIDs := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == schema.AssistantRole {
			for _, tc := range m.ToolCalls {
				calledIDs[tc.ID] = true
			}
		}
	}
	// 收集所有已有 tool_result 的 ID 集合（含重复项，用于判断是否需要补占位）。
	resultIDs := make(map[string]bool)
	for _, m := range msgs {
		if m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}

	result := make([]schema.Message, 0, len(msgs))
	seenResults := make(map[string]bool)
	var pending []schema.Message // 待补的占位 tool_result
	flushPending := func() {
		result = append(result, pending...)
		pending = nil
	}

	for _, m := range msgs {
		if m.ToolCallID != "" {
			// 删除孤立的 tool_result（无对应的 tool_call）与重复结果。
			if !calledIDs[m.ToolCallID] || seenResults[m.ToolCallID] {
				continue
			}
			seenResults[m.ToolCallID] = true
			result = append(result, m)
			continue
		}

		// 遇到非 tool_result 消息时先落盘占位，保证占位排在已有结果之后。
		flushPending()
		result = append(result, m)

		if m.Role != schema.AssistantRole || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !resultIDs[tc.ID] {
				pending = append(pending, schema.Message{
					Role:       schema.UserRole,
					Content:    placeholderToolResult,
					ToolCallID: tc.ID,
				})
			}
		}
	}
	flushPending()
	return result
}

// SlidingWindowCompactor 保留最近 MaxMessages 条消息（System Prompt 固定在首位）。
// MaxMessages 含 system 消息本身；0 或负数时使用默认值 100。
type SlidingWindowCompactor struct {
	MaxMessages int
}

func NewSlidingWindowCompactor(maxMessages int) *SlidingWindowCompactor {
	if maxMessages <= 0 {
		maxMessages = 100
	}
	if maxMessages < 2 {
		maxMessages = 2 // 至少保留 system + 一条对话
	}
	return &SlidingWindowCompactor{
		MaxMessages: maxMessages,
	}
}

func (c *SlidingWindowCompactor) maxMessages() int {
	if c.MaxMessages <= 0 {
		return 100
	}
	if c.MaxMessages < 2 {
		return 2
	}
	return c.MaxMessages
}

func (c *SlidingWindowCompactor) Compact(ctx context.Context, msgs []schema.Message) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return msgs, nil
	}

	max := c.maxMessages()
	if len(msgs) <= max {
		return msgs, nil
	}

	system, rest := splitSystem(msgs)
	keep := max - len(system)
	if keep < 1 {
		keep = 1
	}
	if len(rest) <= keep {
		return msgs, nil
	}

	result := joinMessages(system, rest[len(rest)-keep:])
	return repairOrphanedToolPairs(result), nil
}

// TokenBudgetCompactor 丢弃最旧的对话消息（system 始终保留），
// 直到 token 估算落入预算；至少保留 MinTailMessages 条最近的对话消息。
type TokenBudgetCompactor struct {
	MaxTokens       int
	MinTailMessages int
}

func NewTokenBudgetCompactor(contextWindow int) *TokenBudgetCompactor {
	maxTokens := contextWindow * 80 / 100
	return &TokenBudgetCompactor{
		MaxTokens:       maxTokens,
		MinTailMessages: 6,
	}
}

func (c *TokenBudgetCompactor) maxTokens() int {
	if c.MaxTokens <= 0 {
		return 160_000 // 200K * 80% conservative default
	}
	return c.MaxTokens
}

func (c *TokenBudgetCompactor) minTail() int {
	if c.MinTailMessages <= 0 {
		return 6
	}
	return c.MinTailMessages
}

func (c *TokenBudgetCompactor) Compact(ctx context.Context, msgs []schema.Message) ([]schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return msgs, nil
	}

	maxTokens := c.maxTokens()
	minTail := c.minTail()

	system, rest := splitSystem(msgs)
	if len(rest) <= minTail {
		return msgs, nil
	}
	if EstimateTokens(msgs) <= maxTokens {
		return msgs, nil
	}

	tail := rest[len(rest)-minTail:]

	// 从"保留最多历史"开始逐步丢弃最旧的消息，直到落入预算。
	// token 随 headEnd 单调递减，因此只需增量扣减字符数，无需每轮重建切片。
	headEnd := len(rest) - minTail
	chars := estimateChars(system) + estimateChars(rest[:headEnd]) + estimateChars(tail)
	for {
		if tokensFromChars(chars, len(system)+headEnd+len(tail)) <= maxTokens {
			return repairOrphanedToolPairs(joinMessages(system, rest[:headEnd], tail)), nil
		}
		if headEnd == 0 {
			break
		}
		headEnd--
		chars -= messageChars(rest[headEnd])
	}

	// 即使只保留 system + 最近的 minTail 条也超预算：返回最小可用上下文，
	// 由调用方决定是否继续降级（例如再交给 SummarizationCompactor 或报错）。
	return repairOrphanedToolPairs(joinMessages(system, tail)), nil
}
