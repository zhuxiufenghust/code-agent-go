package engine

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/zhuxiufenghust/code-agent-go/internal/base"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// EventType 枚举了引擎面向客户端的流式事件类型。
type EventType string

const (
	EventDone          EventType = "done"
	EventThinkingDelta EventType = "thinking_delta"
	EventActionDelta   EventType = "action_delta"
	EventToolStart     EventType = "tool_start"
	EventToolResult    EventType = "tool_result"

	// EventError 表示 agent loop 中发生了错误。Data 类型为 string（错误描述）。
	EventError EventType = "error"

	EventApprovalRequired EventType = "approval_required"
)

// ToolResultData 是 EventToolResult 事件的载荷，携带工具执行结果和引擎侧精确耗时。
type ToolResultData struct {
	// Result 是工具执行的结果。
	Result schema.ToolResult
	// Duration 是工具在引擎侧的精确执行耗时（从工具函数入口到返回的真实时长，不含 channel 传输延迟）。
	Duration time.Duration
}

// ApprovalRequest 是 EventApprovalRequired 的事件载荷。
// 引擎 goroutine 通过 ResponseCh 阻塞等待 TUI（或其他消费者）的审批决策。
type ApprovalRequest struct {
	ToolCall  schema.ToolCall
	Reason    string
	RiskLevel string
	// ResponseCh chan hooks.ApprovalResponse
}

type Event struct {
	Type EventType `json:"type"`
	Turn int       `json:"turn,omitempty"`
	// Data 事件载荷，类型随 Type 变化：
	//   EventActionDelta  → string, EventThinkingDelta → string,
	//   EventToolStart    → schema.ToolCall,
	//   EventToolResult   → ToolResultData, EventDone → nil, EventError → string,
	//   EventTokenUpdate  → TokenUpdateData, EventCompaction → CompactionData,
	//   EventSubAgent     → schema.SubAgentUpdate
	Data any `json:"data,omitempty"`
}

func (e *AgentEngine) StreamRun(ctx context.Context, userPrompt string) (<-chan Event, error) {
	ch := make(chan Event)

	base.GoWrap(func() {
		defer close(ch)

		em := emitter{
			generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
				return e.streamGenerate(ctx, ch, turn, history, tools)
			},
			toolStart: func(turn int, tc schema.ToolCall) {
				sendEvent(ctx, ch, Event{Type: EventToolStart, Turn: turn, Data: tc})
			},
			toolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
				sendEvent(ctx, ch, Event{Type: EventToolResult, Turn: turn, Data: ToolResultData{Result: result, Duration: d}})
			},
			tokenUpdate: func(tokens, window int) {
				panic("not imp")
			},
		}

		processCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()

		if err := e.runLoop(processCtx, userPrompt, "agent-stream", em); err != nil {
			ch <- Event{Type: EventError, Data: err.Error()}
			return
		}

	})

	log.Debug("stream_run", zap.String("user_prompt", userPrompt))

	return ch, nil
}

func sendEvent(ctx context.Context, ch chan<- Event, evt Event) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- evt:
		return true
	}
}

func (e *AgentEngine) streamGenerate(ctx context.Context, ch chan<- Event, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	stream, err := e.provider.GenerateStream(ctx, history, tools)
	if err != nil {
		return nil, nil, err
	}

	var msg *schema.Message
	var usage *schema.Usage
	for chunk := range stream {
		switch chunk.Type {
		case schema.StreamChunkTextDelta:
			if !sendEvent(ctx, ch, Event{Type: EventActionDelta, Turn: turn, Data: chunk.Delta}) {
				return nil, nil, ctx.Err()
			}
		case schema.StreamChunkThinkingDelta:
			if !sendEvent(ctx, ch, Event{Type: EventThinkingDelta, Turn: turn, Data: chunk.Delta}) {
				return nil, nil, ctx.Err()
			}
		case schema.StreamChunkDone:
			msg = chunk.Message
			usage = chunk.Usage
		case schema.StreamChunkError:
			return nil, nil, errors.Wrap(chunk.Err, "provider stream error")
		}
	}

	if msg == nil {
		return nil, nil, errors.New("provider stream ended without done chunk")
	}
	return msg, usage, nil
}
