package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"time"
)

// 关键路径上的 Span 名称常量。日志/采集侧按名字聚合，
// 因此这里作为稳定契约导出，而不是让调用方各自拼字符串。
const (
	// SpanRun 是"一次用户输入触发的完整运行"的根 Span。
	SpanRun = "agent.run"
	// SpanLLMGenerate 是一次 LLM 调用（含重试之外的单次尝试）。
	SpanLLMGenerate = "llm.generate"
	// SpanToolPrefix 是工具执行 Span 的名称前缀，后接工具名。
	SpanToolPrefix = "tool."
)

// Attr 是 Span 上的一组键值标记，取值限定为字符串以保证序列化稳定。
type Attr struct {
	Key   string
	Value string
}

// String 构造一个字符串标记。
func String(k, v string) Attr { return Attr{Key: k, Value: v} }

type spanCtxKey struct{}

// Span 描述关键路径上的一段执行。
// 它是"一次性"的：由 StartSpan 创建、由 End 收尾，收尾后字段不再变化。
type Span struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	StartTime    time.Time         `json:"start_time"`
	DurationMS   int64             `json:"duration_ms"`
	Error        string            `json:"error,omitempty"`
	Attrs        map[string]string `json:"attrs,omitempty"`

	sink *core
	done atomic.Bool
}

// StartSpan 在 ctx 上开一个子 Span：若 ctx 中已有 Span，新 Span 继承其 TraceID
// 并把它记为父；否则新开一条 Trace。
// 返回的 ctx 携带该 Span，供下游继续挂子节点。
func (c *core) StartSpan(ctx context.Context, name string, attrs ...Attr) (context.Context, *Span) {
	if ctx == nil {
		// 容忍调用方传 nil（比起 panic，退化成 Background 更符合观测组件"绝不干扰主流程"的定位）。
		ctx = context.Background()
	}
	traceID := newID(32)
	parentID := ""
	if parent := SpanFromContext(ctx); parent != nil {
		traceID = parent.TraceID
		parentID = parent.SpanID
	}
	s := &Span{
		TraceID:      traceID,
		SpanID:       newID(16),
		ParentSpanID: parentID,
		Name:         name,
		StartTime:    time.Now(),
		sink:         c,
	}
	for _, a := range attrs {
		s.SetAttr(a.Key, a.Value)
	}
	return context.WithValue(ctx, spanCtxKey{}, s), s
}

// SpanFromContext 取出 ctx 上挂载的 Span，不存在时返回 nil。
func SpanFromContext(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(spanCtxKey{}).(*Span)
	return s
}

// SetAttr 追加/覆盖一个标记。End 之后再调用无意义（记录已导出）。
func (s *Span) SetAttr(k, v string) {
	if s == nil || k == "" {
		return
	}
	if s.Attrs == nil {
		s.Attrs = make(map[string]string, 4)
	}
	s.Attrs[k] = v
}

// End 收尾 Span：记录耗时与错误，并交给 exporter。
// 幂等——重复调用（或 nil Span 上调用）都不会产生副作用。
func (s *Span) End(err error) {
	if s == nil || !s.done.CompareAndSwap(false, true) {
		return
	}
	s.DurationMS = time.Since(s.StartTime).Milliseconds()
	if err != nil {
		s.Error = err.Error()
	}
	if s.sink != nil {
		s.sink.emitSpan(s)
	}
}

// newID 生成 n 个十六进制字符的随机 ID。
// 用 crypto/rand：trace id 不需要保密，但要做到跨进程不碰撞。
func newID(n int) string {
	buf := make([]byte, n/2)
	if _, err := rand.Read(buf); err != nil {
		// 熵源故障不应该让观测代码panic：退化成定长占位串，链路仍能串起来。
		return strings.Repeat("0", n)
	}
	return hex.EncodeToString(buf)
}
