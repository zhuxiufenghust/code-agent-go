package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

// stubBashTool 是一个名为 bash_tool 的占位工具，用于让审批中间件
// （ApprovalManager 只对 bash/edit/write 生效）真正介入 Execute 流程。
type stubBashTool struct {
	mu       sync.Mutex
	executed []string
}

func (t *stubBashTool) GetDefinition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "bash_tool", Description: "stub bash"}
}

func (t *stubBashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var payload struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &payload)
	t.mu.Lock()
	t.executed = append(t.executed, payload.Command)
	t.mu.Unlock()
	return "executed: " + payload.Command, nil
}

func (t *stubBashTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.executed)
}

// TestStreamRun_ConcurrentApprovals 验证并发的多个高危工具调用会各自发出审批请求，
// 而不是只弹一次窗、其余请求被静默吞掉（并发场景下的核心回归点）。
func TestStreamRun_ConcurrentApprovals(t *testing.T) {
	stub := &stubBashTool{}
	reg := tools.NewRegistry()
	if err := reg.Register(stub); err != nil {
		t.Fatalf("注册工具失败: %v", err)
	}

	// 审批管理器：内置规则会命中 rm -rf，无需配置黑名单。
	approval := tools.NewApprovalManager(nil)
	reg.Use(approval.WaitForApproval)

	// 一轮里并发两个高危调用。
	call := func(id string) schema.ToolCall {
		return schema.ToolCall{ID: id, Name: "bash_tool", Arguments: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)}
	}
	p := &fakeProvider{
		streamScript: []streamStep{
			{msg: &schema.Message{Role: schema.AssistantRole, ToolCalls: []schema.ToolCall{call("c1"), call("c2")}}},
			{msg: &schema.Message{Role: schema.AssistantRole, Content: "done"}},
		},
	}

	e := engine.NewAgentEngine(p, reg)
	approval.SetEmitGetter(e)

	ch, err := e.StreamRun(context.Background(), "delete both")
	if err != nil {
		t.Fatalf("StreamRun 返回错误: %v", err)
	}

	// 边收事件边以"批准"回应每个审批请求，模拟 TUI 逐个确认。
	var approvals []schema.ApprovalRequest
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range ch {
			if evt.Type == engine.EventApprovalRequired {
				ar, ok := evt.Data.(schema.ApprovalRequest)
				if !ok {
					continue
				}
				approvals = append(approvals, ar)
				ar.ResultChannel <- schema.ApprovalResult{Allowed: true, Reason: "用户批准"}
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("事件流未在预期时间内结束（并发审批可能互相阻塞）")
	}

	if len(approvals) != 2 {
		t.Fatalf("并发两个高危调用应产生 2 次审批请求, 实际 %d", len(approvals))
	}
	ids := map[string]bool{}
	for _, ar := range approvals {
		ids[ar.TaskID] = true
	}
	if !ids["c1"] || !ids["c2"] {
		t.Fatalf("两次审批请求应分别对应 c1/c2, 实际 %v", ids)
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("批准后两个工具都应真正执行, 实际执行 %d 次", n)
	}
	_ = strings.TrimSpace("")
}
