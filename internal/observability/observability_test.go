package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

func TestSnapshotAggregatesLLMCalls(t *testing.T) {
	o := Nop()
	o.ObserveLLMCall(100*time.Millisecond, nil)
	o.ObserveLLMCall(300*time.Millisecond, errors.New("boom"))

	s := o.Snapshot()
	if s.LLM.Calls != 2 || s.LLM.Errors != 1 {
		t.Fatalf("calls/errors 不符预期: %+v", s.LLM)
	}
	// 成功率是" observables 的一致性视图 "：分子分母必须来自同一次取快照。
	if got := s.LLM.SuccessRate; got < 0.49 || got > 0.51 {
		t.Fatalf("success_rate 期望 0.5, 实际 %v", got)
	}
	if s.LLM.LatencyMS != 400 {
		t.Fatalf("latency_ms 期望 400, 实际 %d", s.LLM.LatencyMS)
	}
	if s.LLM.AvgLatencyMS != 200 || s.LLM.MaxLatencyMS != 300 {
		t.Fatalf("avg/max latency 不符预期: %+v", s.LLM)
	}
}

func TestSnapshotAggregatesToolCallsByTool(t *testing.T) {
	o := Nop()
	o.ObserveToolCall("bash_tool", 10*time.Millisecond, false)
	o.ObserveToolCall("bash_tool", 20*time.Millisecond, true)
	o.ObserveToolCall("read_tool", 5*time.Millisecond, false)

	s := o.Snapshot()
	if s.Tool.Calls != 3 || s.Tool.Errors != 1 {
		t.Fatalf("工具总计不符预期: %+v", s.Tool)
	}
	bash := s.Tool.ByTool["bash_tool"]
	if bash.Calls != 2 || bash.Errors != 1 || bash.MaxLatencyMS != 20 {
		t.Fatalf("bash_tool 明细不符预期: %+v", bash)
	}
	if _, ok := s.Tool.ByTool["read_tool"]; !ok {
		t.Fatal("read_tool 明细缺失")
	}
}

func TestObserveUsageOverwritesCumulativeTokens(t *testing.T) {
	o := Nop()
	// usage 由引擎按"累计快照"上报，因此后到的值直接覆盖，而不是累加。
	o.ObserveUsage(schema.Usage{InputTokens: 100, OutputTokens: 20})
	o.ObserveUsage(schema.Usage{InputTokens: 150, OutputTokens: 40})

	s := o.Snapshot()
	if s.Token.Input != 150 || s.Token.Output != 40 || s.Token.Total != 190 {
		t.Fatalf("累计 token 不符预期: %+v", s.Token)
	}
}

func TestObserveCompaction(t *testing.T) {
	o := Nop()
	o.ObserveCompaction(1000, 400)
	o.ObserveCompaction(2000, 500)

	s := o.Snapshot()
	if s.Compaction.Count != 2 || s.Compaction.TokensBefore != 3000 || s.Compaction.TokensAfter != 900 {
		t.Fatalf("压缩指标不符预期: %+v", s.Compaction)
	}
	if s.Compaction.TokensSaved != 2100 {
		t.Fatalf("节省 token 计算错误: %d", s.Compaction.TokensSaved)
	}
}

func TestStdoutExporterDumpsOnClose(t *testing.T) {
	var buf bytes.Buffer
	o := NewStdout(&buf, time.Hour) // 周期设得很长，靠 Close 触发最后一次导出
	o.ObserveLLMCall(50*time.Millisecond, nil)
	o.Close()

	lines := nonEmptyLines(t, buf.String())
	if len(lines) != 1 {
		t.Fatalf("期望导出 1 行, 实际 %d 行: %q", len(lines), buf.String())
	}
	var rec struct {
		Type     string   `json:"type"`
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("导出不是合法 JSON: %v (%s)", err, lines[0])
	}
	if rec.Type != "metrics" {
		t.Fatalf("type 期望 metrics, 实际 %q", rec.Type)
	}
	if rec.Snapshot.LLM.Calls != 1 {
		t.Fatalf("快照未携带 LLM 指标: %+v", rec.Snapshot.LLM)
	}
}

func TestStdoutExporterEmitsSpan(t *testing.T) {
	var buf bytes.Buffer
	o := NewStdout(&buf, time.Hour)
	_, span := o.StartSpan(nil, SpanLLMGenerate, String("turn", "3"))
	span.End(errors.New("api timeout"))
	o.Close()

	records := decodeLines(t, buf.String())
	var spanRec struct {
		Type string `json:"type"`
		Span Span   `json:"span"`
	}
	var found bool
	for _, line := range records {
		if err := json.Unmarshal([]byte(line), &spanRec); err != nil {
			t.Fatalf("导出不是合法 JSON: %v", err)
		}
		if spanRec.Type != "span" {
			continue
		}
		found = true
		if spanRec.Span.Name != SpanLLMGenerate || spanRec.Span.Attrs["turn"] != "3" {
			t.Fatalf("span 内容不符预期: name=%q attrs=%v", spanRec.Span.Name, spanRec.Span.Attrs)
		}
		if spanRec.Span.Error != "api timeout" {
			t.Fatalf("期望带上错误信息, 实际 %q", spanRec.Span.Error)
		}
		if spanRec.Span.ParentSpanID != "" {
			t.Fatalf("无父 Span 时不该有 parent_span_id: %q", spanRec.Span.ParentSpanID)
		}
	}
	if !found {
		t.Fatalf("未导出 span 记录: %q", buf.String())
	}
}

func TestSpanHierarchySharesTraceID(t *testing.T) {
	o := Nop()
	ctx, root := o.StartSpan(nil, SpanRun)
	ctx, child := o.StartSpan(ctx, SpanLLMGenerate)

	if child.TraceID != root.TraceID {
		t.Fatalf("子 Span 应继承父 TraceID: %s vs %s", child.TraceID, root.TraceID)
	}
	if child.ParentSpanID != root.SpanID {
		t.Fatalf("子 Span 父 SpanID 错误: %s vs %s", child.ParentSpanID, root.SpanID)
	}
	if got := SpanFromContext(ctx); got != child {
		t.Fatal("SpanFromContext 未能取回刚创建的 Span")
	}
}

// TestSpanEndIdempotent 保证重复收尾不会产生两条记录：
// 调用方为了保险用 defer 收尾很常见，重复导出会让指标翻倍。
func TestSpanEndIdempotent(t *testing.T) {
	var buf bytes.Buffer
	o := NewStdout(&buf, time.Hour)
	_, span := o.StartSpan(nil, SpanRun)
	span.End(nil)
	span.End(errors.New("太迟了"))
	o.Close()

	var n int
	for _, line := range decodeLines(t, buf.String()) {
		if strings.Contains(line, `"type":"span"`) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重复 End 应只导出一次, 实际 %d 次", n)
	}
}

func TestNopIsSafe(t *testing.T) {
	o := Nop()
	ctx, span := o.StartSpan(nil, SpanRun)
	child := SpanFromContext(ctx)
	child.End(nil)
	span.End(nil) // 重复收尾
	o.Close()     // 幂等关闭
	o.Close()
	// nil Span 上 End 也不该 panic（调用方常写 defer span.End(err)）
	var nilSpan *Span
	nilSpan.End(nil)
}

func nonEmptyLines(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func decodeLines(t *testing.T, s string) []string {
	t.Helper()
	lines := nonEmptyLines(t, s)
	if len(lines) == 0 {
		t.Fatal("没有任何输出")
	}
	return lines
}
