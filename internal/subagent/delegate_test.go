package subagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

// scriptedProvider 按脚本逐次返回响应：每次 GenerateStream 取一条。
// 只有"真接一段引擎"才算验证了委派链路，只测参数解析会发现不了
// "子 Agent 根本跑不起来"这类问题。
type scriptedProvider struct {
	mu    sync.Mutex
	idx   int
	steps []schema.Message
}

func (p *scriptedProvider) next() schema.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	msg := p.steps[p.idx%len(p.steps)]
	p.idx++
	return msg
}

func (p *scriptedProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	return provider.GenerateFromStream(ctx, p.GenerateStream, msgs, availableTools)
}

func (p *scriptedProvider) GenerateStream(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (<-chan schema.StreamChunk, error) {
	msg := p.next()
	ch := make(chan schema.StreamChunk, 4)
	go func() {
		defer close(ch)
		if msg.Content != "" {
			ch <- schema.StreamChunk{Type: schema.StreamChunkTextDelta, Delta: msg.Content}
		}
		out := msg
		ch <- schema.StreamChunk{Type: schema.StreamChunkDone, Message: &out, Usage: &schema.Usage{InputTokens: 1, OutputTokens: 1}}
	}()
	return ch, nil
}

func (p *scriptedProvider) GetUsage() schema.Usage { return schema.Usage{} }

func textStep(s string) schema.Message {
	return schema.Message{Role: schema.AssistantRole, Content: s}
}

func newTestTool(t *testing.T, cfg Config, steps ...schema.Message) *Tool {
	t.Helper()
	reg := tools.NewRegistry()
	if err := reg.Register(tools.NewReadTool(t.TempDir())); err != nil {
		t.Fatalf("注册 read 工具失败: %v", err)
	}
	return New(cfg, reg, &scriptedProvider{steps: steps})
}

func TestDelegateTool_SingleTaskReturnsSummary(t *testing.T) {
	tool := newTestTool(t, Config{MaxDepth: 1, MaxConcurrent: 2, MaxTurns: 4, MaxOutputChars: 500, Timeout: 0},
		textStep("结论：三个调用点"))
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"找出所有调用点"}`))
	if err != nil {
		t.Fatalf("委派执行失败: %v", err)
	}
	var report struct {
		Delegated int          `json:"delegated"`
		Results   []TaskResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("结果不是合法 JSON: %v (%s)", err, out)
	}
	if report.Delegated != 1 || len(report.Results) != 1 {
		t.Fatalf("结果数量不符: %+v", report)
	}
	if report.Results[0].Summary != "结论：三个调用点" {
		t.Fatalf("结论不符: %q", report.Results[0].Summary)
	}
}

func TestDelegateTool_ParallelTasksKeepInputOrder(t *testing.T) {
	tool := newTestTool(t, Config{MaxDepth: 1, MaxConcurrent: 2, MaxTurns: 4, MaxOutputChars: 500, Timeout: 0},
		textStep("A 的结论"), textStep("B 的结论"))
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"tasks":[{"id":"a","task":"找 A"},{"id":"b","task":"找 B"}]}`))
	if err != nil {
		t.Fatalf("并行委派失败: %v", err)
	}
	var report struct {
		Results []TaskResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("应有两条结果: %+v", report)
	}
	// 主 Agent 靠位置对应任务与结论，顺序乱了就等于结论张冠李戴。
	if report.Results[0].ID != "a" || report.Results[1].ID != "b" {
		t.Fatalf("结果顺序被打乱: %s, %s", report.Results[0].ID, report.Results[1].ID)
	}
	for _, r := range report.Results {
		if r.Summary == "" {
			t.Fatalf("存在空结论: %+v", r)
		}
	}
}

// TestDelegateTool_ChildCannotDelegate 是"最大嵌套深度"的核心断言：
// 子 Agent 的工具视图里没有委派工具，绕过视图硬调也会被拒绝，
// 而不是无限套娃把 token 烧掉。
func TestDelegateTool_ChildCannotDelegate(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(tools.NewReadTool(t.TempDir())); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	tool := New(Config{MaxDepth: 1, MaxConcurrent: 1, MaxTurns: 4, MaxOutputChars: 500, Timeout: 0},
		reg, &scriptedProvider{steps: []schema.Message{textStep("子 Agent 的结论")}})

	view := newExcludingRegistry(reg, []string{ToolName})
	for _, def := range view.GetAvailableTools() {
		if def.Name == ToolName {
			t.Fatalf("子 Agent 仍能看到委派工具")
		}
	}
	res := view.Execute(context.Background(), schema.ToolCall{ID: "c1", Name: ToolName, Arguments: json.RawMessage(`{}`)})
	if !res.IsError || !strings.Contains(res.Output, ToolName) {
		t.Fatalf("绕过视图调用委派工具应被拒绝: %+v", res)
	}
	// 正常委派仍要跑得通：拒绝嵌套不应连带把一次合法委派搞坏。
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"找调用点"}`)); err != nil {
		t.Fatalf("合法委派失败: %v", err)
	}
}

func TestDelegateTool_RejectsBadInput(t *testing.T) {
	tool := newTestTool(t, Config{MaxDepth: 1, MaxConcurrent: 1, MaxTurns: 4, MaxOutputChars: 200, Timeout: 0}, textStep("x"))
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":`)); err == nil {
		t.Fatal("非法 JSON 应当报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"   "}`)); err == nil {
		t.Fatal("空任务应当报错")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"tasks":[{"id":"a"},{"id":"b"}]}`)); err == nil {
		t.Fatal("缺少 task 字段应当报错")
	}
	if _, err := tool.Execute(context.Background(),
		json.RawMessage(`{"tasks":[{"id":"a","task":"A"},{"id":"b","task":"B"}]}`)); err == nil ||
		!strings.Contains(err.Error(), "并发上限") {
		t.Fatalf("超出并发上限应当报错, 实际 %v", err)
	}
}

func TestDelegateTool_DisabledByMaxDepth(t *testing.T) {
	tool := newTestTool(t, Config{MaxDepth: 0, MaxConcurrent: 1}, textStep("x"))
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"A"}`)); err == nil {
		t.Fatal("MaxDepth<=0 时应当拒绝委派")
	}
}

func TestDelegateTool_DefaultsAndDefinition(t *testing.T) {
	cfg := Config{MaxDepth: 1}.withDefaults()
	if cfg.MaxConcurrent != DefaultMaxConcurrent || cfg.MaxTurns != DefaultMaxTurns ||
		cfg.MaxOutputChars != DefaultMaxOutputChars || cfg.Timeout != DefaultTimeout {
		t.Fatalf("缺省预算未补齐: %+v", cfg)
	}
	def := newTestTool(t, Config{MaxDepth: 1}).GetDefinition()
	if def.Name != ToolName || def.Description == "" || def.InputSchema == nil {
		t.Fatalf("工具定义不合规: %+v", def)
	}
}
