package observability

import (
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// writeJSONLine 把任意值写成一行 JSON（末尾补 \n）。
// 写失败只在日志层面可见：观测数据的丢失绝不能影响主流程。
func writeJSONLine(w io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// Snapshot 是当前累计指标的一致性快照：结构体按值返回，
// 调用方可以随意读而不会再被后续的 Observe 改动。
type Snapshot struct {
	// Since 是本轮采集的起始时刻。
	Since time.Time `json:"since"`
	// ElapsedMS 是自 Since 起的累计时长。
	ElapsedMS  int64              `json:"elapsed_ms"`
	LLM        LLMSnapshot        `json:"llm"`
	Tool       ToolSnapshot       `json:"tool"`
	Token      TokenSnapshot      `json:"token"`
	Compaction CompactionSnapshot `json:"compaction"`
}

// LLMSnapshot 是 LLM 调用维度的累计指标。
type LLMSnapshot struct {
	Calls        int64   `json:"calls"`
	Errors       int64   `json:"errors"`
	SuccessRate  float64 `json:"success_rate"`
	LatencyMS    int64   `json:"latency_ms"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	MaxLatencyMS int64   `json:"max_latency_ms"`
}

// ToolSnapshot 是工具执行维度的累计指标（成功率取整体，分工具明细在 ByTool）。
type ToolSnapshot struct {
	Calls        int64               `json:"calls"`
	Errors       int64               `json:"errors"`
	SuccessRate  float64             `json:"success_rate"`
	LatencyMS    int64               `json:"latency_ms"`
	AvgLatencyMS int64               `json:"avg_latency_ms"`
	ByTool       map[string]ToolStat `json:"by_tool,omitempty"`
}

// ToolStat 是单个工具的累计指标。
type ToolStat struct {
	Calls        int64   `json:"calls"`
	Errors       int64   `json:"errors"`
	SuccessRate  float64 `json:"success_rate"`
	LatencyMS    int64   `json:"latency_ms"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	MaxLatencyMS int64   `json:"max_latency_ms"`
}

// TokenSnapshot 是会话累计 token 用量（来自引擎每次 LLM 调用后的累计快照）。
type TokenSnapshot struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Total  int64 `json:"total_tokens"`
}

// CompactionSnapshot 描述上下文压缩发生的次数与被砍掉的估算 token 量。
type CompactionSnapshot struct {
	Count        int64 `json:"count"`
	TokensBefore int64 `json:"tokens_before"`
	TokensAfter  int64 `json:"tokens_after"`
	TokensSaved  int64 `json:"tokens_saved"`
}

// recorder 是线程安全的指标累加器，被所有 Observability 实现复用。
type recorder struct {
	mu    sync.Mutex
	since time.Time

	llmCalls   int64
	llmErrors  int64
	llmLatency time.Duration
	llmMaxLat  time.Duration

	toolCalls   int64
	toolErrors  int64
	toolLatency time.Duration
	tools       map[string]*toolCounter

	tokens        schema.Usage
	compactions   int64
	compactBefore int64
	compactAfter  int64
}

type toolCounter struct {
	calls   int64
	errors  int64
	latency time.Duration
	maxLat  time.Duration
}

func newRecorder() *recorder {
	return &recorder{
		since: time.Now(),
		tools: make(map[string]*toolCounter),
	}
}

func (r *recorder) ObserveLLMCall(d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.llmCalls++
	if err != nil {
		r.llmErrors++
	}
	r.llmLatency += d
	if d > r.llmMaxLat {
		r.llmMaxLat = d
	}
}

func (r *recorder) ObserveToolCall(toolName string, d time.Duration, isError bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCalls++
	r.toolLatency += d
	if isError {
		r.toolErrors++
	}
	c := r.tools[toolName]
	if c == nil {
		c = &toolCounter{}
		r.tools[toolName] = c
	}
	c.calls++
	c.latency += d
	if d > c.maxLat {
		c.maxLat = d
	}
	if isError {
		c.errors++
	}
}

func (r *recorder) ObserveUsage(usage schema.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = usage
}

func (r *recorder) ObserveCompaction(tokensBefore, tokensAfter int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compactions++
	r.compactBefore += int64(tokensBefore)
	r.compactAfter += int64(tokensAfter)
}

// Snapshot 取当前快照。所有比率都在取快照时计算，避免把
// "分子分母分别更新、读到的比率不自洽"的窗口暴露给观测方。
func (r *recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := Snapshot{
		Since:     r.since,
		ElapsedMS: time.Since(r.since).Milliseconds(),
		Token: TokenSnapshot{
			Input:  int64(r.tokens.InputTokens),
			Output: int64(r.tokens.OutputTokens),
			Total:  int64(r.tokens.InputTokens + r.tokens.OutputTokens),
		},
		Compaction: CompactionSnapshot{
			Count:        r.compactions,
			TokensBefore: r.compactBefore,
			TokensAfter:  r.compactAfter,
			TokensSaved:  r.compactBefore - r.compactAfter,
		},
	}

	s.LLM = LLMSnapshot{
		Calls:        r.llmCalls,
		Errors:       r.llmErrors,
		SuccessRate:  successRate(r.llmCalls, r.llmErrors),
		LatencyMS:    r.llmLatency.Milliseconds(),
		AvgLatencyMS: avgMS(r.llmLatency, r.llmCalls),
		MaxLatencyMS: r.llmMaxLat.Milliseconds(),
	}

	s.Tool = ToolSnapshot{
		Calls:        r.toolCalls,
		Errors:       r.toolErrors,
		SuccessRate:  successRate(r.toolCalls, r.toolErrors),
		LatencyMS:    r.toolLatency.Milliseconds(),
		AvgLatencyMS: avgMS(r.toolLatency, r.toolCalls),
	}
	if len(r.tools) > 0 {
		s.Tool.ByTool = make(map[string]ToolStat, len(r.tools))
		for name, c := range r.tools {
			s.Tool.ByTool[name] = ToolStat{
				Calls:        c.calls,
				Errors:       c.errors,
				SuccessRate:  successRate(c.calls, c.errors),
				LatencyMS:    c.latency.Milliseconds(),
				AvgLatencyMS: avgMS(c.latency, c.calls),
				MaxLatencyMS: c.maxLat.Milliseconds(),
			}
		}
	}
	return s
}

// successRate 计算成功率。无样本时返回 0——"还没调用过"与"全部失败"
// 在比率上是同一回事，但 Calls 会说明是哪种，这里不做无样本置 1 的美化。
func successRate(calls, failures int64) float64 {
	if calls == 0 {
		return 0
	}
	return float64(calls-failures) / float64(calls)
}

func avgMS(total time.Duration, count int64) int64 {
	if count == 0 {
		return 0
	}
	return total.Milliseconds() / count
}
