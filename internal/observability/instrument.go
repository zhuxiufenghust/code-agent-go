package observability

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// Instrument 把观测埋点织进引擎的事件回调，返回一个功能等价的 Emitter：
// 原回调保持原样（nil 也仍是 nil 语义之外的部分见下），只是额外记录指标与 Span。
//
// 放在这里而不是写进 agent_loop 的理由：观测属于横切关注点，
// 主循环里到处散落 m.ObserveXXX 会让业务代码被埋点淹没，也不会被编译器保证覆盖完整。
//
// parent 用于给 Span 提供调用链父节点：Emitter 的工具回调（ToolStart/ToolDone）
// 签名里没有 ctx（接口约束），因此工具 Span 统一挂在 parent 之上。
func Instrument(parent context.Context, em report.Emitter, o Observability) report.Emitter {
	if o == nil {
		o = Nop()
	}
	if parent == nil {
		parent = context.Background()
	}
	out := em

	if em.Generate != nil {
		out.Generate = func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			ctx, span := o.StartSpan(ctx, SpanLLMGenerate, String("turn", strconv.Itoa(turn)))
			start := time.Now()
			msg, usage, err := em.Generate(ctx, turn, history, tools)
			span.End(err)
			o.ObserveLLMCall(time.Since(start), err)
			return msg, usage, err
		}
	}

	// 工具 Span 的生命周期跨 ToolStart/ToolDone 两个回调，按 tool_call_id 关连。
	// 未配对（ToolDone 未被调用，例如进程崩溃）的条目会残留到进程结束，
	// 数量上界等于并发工具数，可接受。
	var (
		mu    sync.Mutex
		spans = make(map[string]*Span)
	)

	// ToolStart/ToolDone 即使原 Emitter 没提供也必须接上去：
	// 工具成功率是本部门最关心的稳定性指标，不能因为"没人看事件"就不采集。
	out.ToolStart = func(turn int, tc schema.ToolCall) {
		_, span := o.StartSpan(parent, SpanToolPrefix+tc.Name,
			String("tool.name", tc.Name), String("tool_call_id", tc.ID))
		mu.Lock()
		spans[tc.ID] = span
		mu.Unlock()
		if em.ToolStart != nil {
			em.ToolStart(turn, tc)
		}
	}
	out.ToolDone = func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
		mu.Lock()
		span := spans[tc.ID]
		delete(spans, tc.ID)
		mu.Unlock()

		var err error
		if result.IsError {
			err = errToolFailed
		}
		span.End(err)
		o.ObserveToolCall(tc.Name, d, result.IsError)
		if em.ToolDone != nil {
			em.ToolDone(turn, tc, result, d)
		}
	}

	if src := em.TokenUpdate; src != nil {
		out.TokenUpdate = func(ctx context.Context, turn int, usage *schema.Usage) {
			observeUsage(o, usage)
			src(ctx, turn, usage)
		}
	} else {
		out.TokenUpdate = func(ctx context.Context, turn int, usage *schema.Usage) {
			observeUsage(o, usage)
		}
	}

	if em.Compaction != nil {
		out.Compaction = func(data report.CompactionData) {
			o.ObserveCompaction(data.TokensBefore, data.TokensAfter)
			em.Compaction(data)
		}
	} else {
		out.Compaction = func(data report.CompactionData) {
			o.ObserveCompaction(data.TokensBefore, data.TokensAfter)
		}
	}

	return out
}

var errToolFailed = errors.New("tool result is_error")

func observeUsage(o Observability, usage *schema.Usage) {
	if usage == nil {
		return
	}
	o.ObserveUsage(*usage)
}
