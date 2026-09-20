package engine

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/zhuxiufenghust/code-agent-go/internal/base"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/report"
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

	// EventFinal 表示 runLoop 主动收尾（达到最大轮次 / 判定重复调用卡住）。
	// Data 类型为 string（给用户的最终文本说明）。
	EventFinal EventType = "final"

	EventUsage EventType = "usage"
)

// defaultRunTimeout 是单轮流式运行的总时长上限。
// 注意：等待人工审批的时间不计入（见 pausableTimeoutCtx），
// 因此这里可以保持较短，用来兜住“模型/工具卡死”而不是“人慢”。
const defaultRunTimeout = time.Minute

// ToolResultData 是 EventToolResult 事件的载荷，携带工具执行结果和引擎侧精确耗时。
type ToolResultData struct {
	// Result 是工具执行的结果。
	Result schema.ToolResult
	// Duration 是工具在引擎侧的精确执行耗时（从工具函数入口到返回的真实时长，不含 channel 传输延迟）。
	Duration time.Duration
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
	// 整轮运行的总时长上限。人工审批等待由 pauser 暂停计时，不计入该配额。
	// 必须在 emitter 之前创建：emitter 的审批回调需要它来暂停/恢复计时。
	processCtx, pauser, cancelProcess := newPausableTimeout(ctx, defaultRunTimeout)
	em := report.Emitter{
		Generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			return e.streamGenerate(ctx, ch, turn, history, tools)
		},
		ToolStart: func(turn int, tc schema.ToolCall) {
			sendEvent(ctx, ch, Event{Type: EventToolStart, Turn: turn, Data: tc})
		},
		ToolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			sendEvent(ctx, ch, Event{Type: EventToolResult, Turn: turn, Data: ToolResultData{Result: result, Duration: d}})
		},
		ApprovalRequired: func(turn int, ar schema.ApprovalRequest) bool {
			// 等待人类决策期间暂停整轮运行计时，否则用户思考/操作的时间会被计入上限。
			pauser.Pause()
			if ok := sendEvent(ctx, ch, Event{
				Type: EventApprovalRequired,
				Turn: turn,
				Data: ar,
			}); !ok {
				// 事件没能送达（事件流已关闭或已取消）：立刻恢复计时。
				// 调用方会据此直接按拒绝处理，不会再回调 ApprovalResolved。
				pauser.Resume()
				return false
			}
			return true
		},
		// 人工审批有结果后（含超时/取消）由 ApprovalManager 回调，恢复运行计时。
		ApprovalResolved: pauser.Resume,
		Final: func(text string) {
			sendEvent(ctx, ch, Event{Type: EventFinal, Data: text})
		},
		TokenUpdate: func(ctx context.Context, turn int, usage *schema.Usage) {
			sendEvent(ctx, ch, Event{Type: EventUsage, Turn: 0, Data: usage})
		},
	}

	e.UpdateEmitter(em)

	base.GoWrap(func() {
		defer close(ch)
		defer cancelProcess()

		if err := e.runLoop(processCtx, userPrompt, "agent-stream"); err != nil {
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
