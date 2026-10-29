// Package observability 提供"量化一次运行情况"所需的最小设施：
// 指标聚合（Metrics）与关键路径埋点（Tracer/Span），以及二者落地的方式（Exporter）。
//
// 设计取舍：暂不引入 Prometheus / OpenTelemetry 依赖，先用两个自带实现跑通闭环——
//   - Nop：默认零产出，让"没配观测"不等于"观测代码到处 nil 判断"；
//   - NewStdout：按 JSON 行输出到任意 io.Writer，容器里直接被采集侧读走。
//
// 接口形状刻意保持"可替换"：后续要接 otel，只需再实现一个 Observability 即可，
// 引擎侧不需要改动（它只依赖 interface）。
package observability

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// Metrics 聚合一次运行过程中的累计指标。所有实现必须并发安全。
type Metrics interface {
	// ObserveLLMCall 记录一次 LLM 调用的耗时与成败（err != nil 计为失败）。
	ObserveLLMCall(d time.Duration, err error)
	// ObserveToolCall 记录一次工具执行的耗时与成败（依据 ToolResult.IsError）。
	ObserveToolCall(toolName string, d time.Duration, isError bool)
	// ObserveUsage 记录会话累计用量。入参是"累计快照"而非本次增量
	// （引擎侧的 usage 本身就是累积值），因此实现应当直接覆盖当前值。
	ObserveUsage(usage schema.Usage)
	// ObserveCompaction 记录一次有效上下文压缩前后的估算 token 数。
	ObserveCompaction(tokensBefore, tokensAfter int)
	// Snapshot 返回当前累计指标快照，零值实现也必须可用。
	Snapshot() Snapshot
}

// Tracer 在关键路径上开 Span，把"一次用户输入 → 多轮 LLM/工具"串成一棵调用树。
type Tracer interface {
	StartSpan(ctx context.Context, name string, attrs ...Attr) (context.Context, *Span)
}

// Observability 是观测组件对外暴露的能力集合：指标 + 埋点 + 生命周期。
type Observability interface {
	Metrics
	Tracer
	// Close 停止后台导出并保证已采集的数据被导出一次；可重复调用。
	// Nop 实现为空操作。
	Close()
}

// DefaultDumpInterval 是 stdout exporter 的默认导出周期。
// 30s 兼顾"长跑能看出趋势"与"短任务退出时 Close 还有最后一次落盘"。
const DefaultDumpInterval = 30 * time.Second

// Nop 返回零产出实现：指标照常聚合（便于测试断言），但不向任何地方写出。
// 默认的 Stdout/nop 之争在这里的结论是——进程默认用 nop，
// 由 cmd 层按配置决定是否换成 stdout。
func Nop() Observability { return &core{rec: newRecorder()} }

// NewStdout 返回一个把指标快照与 Span 记录按 JSON 行写到 w 的观测器。
// w 为 nil 时退化为 Nop；interval <= 0 时取 DefaultDumpInterval。
//
// 输出的每一行都是独立 JSON 对象，便于 jq / Filebeat / 日志采集直接消费：
//
//	{"type":"metrics","snapshot":{...}}
//	{"type":"span","trace_id":"...","name":"llm.generate","duration_ms":812,...}
func NewStdout(w io.Writer, interval time.Duration) Observability {
	if w == nil {
		return Nop()
	}
	if interval <= 0 {
		interval = DefaultDumpInterval
	}
	c := &core{
		rec:      newRecorder(),
		out:      w,
		interval: interval,
		stop:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.dumpLoop()
	return c
}

// core 是当前唯一的 Observability 实现：把指标聚在 recorder 里，
// 由 out 决定"导出还是丢弃"，两者的语义差异只在这一个字段上。
type core struct {
	rec     *recorder
	out     io.Writer // nil 表示不导出
	outMu   sync.Mutex
	wg      sync.WaitGroup
	stop    chan struct{}
	stopped sync.Once

	interval time.Duration
}

func (c *core) ObserveLLMCall(d time.Duration, err error) { c.rec.ObserveLLMCall(d, err) }

func (c *core) ObserveToolCall(toolName string, d time.Duration, isError bool) {
	c.rec.ObserveToolCall(toolName, d, isError)
}

func (c *core) ObserveUsage(usage schema.Usage) { c.rec.ObserveUsage(usage) }

func (c *core) ObserveCompaction(tokensBefore, tokensAfter int) {
	c.rec.ObserveCompaction(tokensBefore, tokensAfter)
}

func (c *core) Snapshot() Snapshot { return c.rec.Snapshot() }

// dumpLoop 周期性地把快照导出；stop 关闭时先补最后一次再退出，
// 避免"短任务一跑完就退出、数据永远等不到第一个 tick"。
func (c *core) dumpLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.dump()
		case <-c.stop:
			c.dump()
			return
		}
	}
}

func (c *core) dump() {
	if c.out == nil {
		return
	}
	line := struct {
		Type     string   `json:"type"`
		Snapshot Snapshot `json:"snapshot"`
	}{Type: "metrics", Snapshot: c.rec.Snapshot()}
	c.outMu.Lock()
	defer c.outMu.Unlock()
	writeJSONLine(c.out, line)
}

// emitSpan 由 Span.End 调用：把一条 Span 记录写到输出端。
func (c *core) emitSpan(s *Span) {
	if c.out == nil {
		return
	}
	line := struct {
		Type string `json:"type"`
		Span *Span  `json:"span"`
	}{Type: "span", Span: s}
	c.outMu.Lock()
	defer c.outMu.Unlock()
	writeJSONLine(c.out, line)
}

func (c *core) Close() {
	if c.stop == nil {
		return // Nop 实例，没有后台 goroutine
	}
	c.stopped.Do(func() { close(c.stop) })
	c.wg.Wait()
}
