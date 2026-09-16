package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type stubEmitterGetter struct {
	em report.Emitter
}

func (s *stubEmitterGetter) GetEmitter() report.Emitter { return s.em }

// TestWaitForApprovalEmitsRequest 验证高危命令会真的发出审批请求并阻塞等待人类决策，
// 而不是直接默认放行（曾因用未被赋值的 ar 字段做开关而永远走“默认通过”）。
func TestWaitForApprovalEmitsRequest(t *testing.T) {
	getter := &stubEmitterGetter{}
	h := NewApprovalManager([]string{"ls -la", `rm -rf \*`})
	h.SetEmitGetter(getter)

	reqCh := make(chan schema.ApprovalRequest, 1)
	getter.em = report.Emitter{
		ApprovalRequired: func(turn int, ar schema.ApprovalRequest) bool {
			reqCh <- ar
			return true
		},
	}

	call := schema.ToolCall{ID: "call-1", Name: "bash_tool", Arguments: []byte(`{"command":"ls -la"}`)}
	done := make(chan schema.ApprovalResult, 1)
	go func() {
		res, err := h.WaitForApproval(context.Background(), call)
		if err != nil {
			t.Errorf("WaitForApproval 返回错误: %v", err)
		}
		done <- res
	}()

	var ar schema.ApprovalRequest
	select {
	case ar = <-reqCh:
		if ar.TaskID != "call-1" {
			t.Fatalf("期望 TaskID=call-1, 实际 %q", ar.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("高危命令未发出审批请求")
	}

	// 未决策前不应返回
	select {
	case res := <-done:
		t.Fatalf("人类未决策就返回了: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}

	ar.ResultChannel <- schema.ApprovalResult{Allowed: false, Reason: "用户拒绝"}

	select {
	case res := <-done:
		if res.Allowed {
			t.Fatal("期望拒绝结果")
		}
	case <-time.After(time.Second):
		t.Fatal("决策已回传但 WaitForApproval 未返回")
	}
}

// TestWaitForApprovalUndelivered 验证审批请求没送达（事件流已关闭/已取消）时立即拒绝，
// 而不是继续空等到超时。
func TestWaitForApprovalUndelivered(t *testing.T) {
	getter := &stubEmitterGetter{}
	h := NewApprovalManager([]string{"ls"}, WithApprovalTimeout(50*time.Millisecond))
	h.SetEmitGetter(getter)

	var resolved bool
	getter.em = report.Emitter{
		ApprovalRequired: func(turn int, ar schema.ApprovalRequest) bool {
			return false
		},
		ApprovalResolved: func() { resolved = true },
	}

	call := schema.ToolCall{ID: "call-3", Name: "bash_tool", Arguments: []byte(`{"command":"ls"}`)}
	start := time.Now()
	res, err := h.WaitForApproval(context.Background(), call)
	if err != nil {
		t.Fatalf("未送达不应返回错误: %v", err)
	}
	if res.Allowed {
		t.Fatal("未送达应按拒绝处理")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("未送达应立即返回, 实际耗时 %v", time.Since(start))
	}
	if resolved {
		t.Fatal("未送达不应回调 ApprovalResolved（无人会再恢复计时）")
	}
}

// TestWaitForApprovalTimeout 验证人类一直不决策时会按超时拒绝，而不是永久悬挂。
func TestWaitForApprovalTimeout(t *testing.T) {
	getter := &stubEmitterGetter{}
	h := NewApprovalManager([]string{"ls"}, WithApprovalTimeout(30*time.Millisecond))
	h.SetEmitGetter(getter)

	gotDeadline := make(chan time.Time, 1)
	var resolved bool
	getter.em = report.Emitter{
		ApprovalRequired: func(turn int, ar schema.ApprovalRequest) bool {
			gotDeadline <- ar.Deadline
			return true
		},
		ApprovalResolved: func() { resolved = true },
	}

	call := schema.ToolCall{ID: "call-4", Name: "bash_tool", Arguments: []byte(`{"command":"ls"}`)}
	res, err := h.WaitForApproval(context.Background(), call)
	if err == nil || res.Allowed {
		t.Fatalf("超时应返回错误且按拒绝处理, 实际 res=%+v err=%v", res, err)
	}
	if d := <-gotDeadline; d.IsZero() {
		t.Fatal("审批请求应携带截止时间，供 TUI 展示倒计时")
	}
	if !resolved {
		t.Fatal("结束后必须回调 ApprovalResolved 恢复运行计时")
	}
}

// TestWaitForApprovalWithoutEmitter 验证没有审批通道（非流式/无头/无 TUI）时按 deny 处理：
// 无人看管的场景下放行高危命令比直接拒绝更危险。
func TestWaitForApprovalWithoutEmitter(t *testing.T) {
	h := NewApprovalManager([]string{"ls"})
	call := schema.ToolCall{ID: "call-2", Name: "bash_tool", Arguments: []byte(`{"command":"ls"}`)}
	res, err := h.WaitForApproval(context.Background(), call)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if res.Allowed {
		t.Fatalf("无审批通道时应默认拒绝, 实际 %+v", res)
	}
}

// TestIsDangerousCommandBuiltinRules 验证内置高危规则开箱生效（不依赖配置黑名单）。
func TestIsDangerousCommandBuiltinRules(t *testing.T) {
	h := NewApprovalManager(nil)
	dangerous := []string{"rm -rf /tmp/x", "git reset --hard", "git push -f origin main", "echo 'DROP TABLE t' | mysql"}
	for _, cmd := range dangerous {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if !h.IsDangerousCommand("bash_tool", string(args)) {
			t.Errorf("内置规则应判为危险: %q", cmd)
		}
	}
	safe := []string{"ls -la", "go test ./...", "cat README.md"}
	for _, cmd := range safe {
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if h.IsDangerousCommand("bash_tool", string(args)) {
			t.Errorf("不应判为危险: %q", cmd)
		}
	}
	// 非 bash 的读写工具维持 YOLO：只匹配 bash 命令，避免误伤
	if h.IsDangerousCommand("read_tool", `{"path":"a.go"}`) {
		t.Error("读取类工具不应触发审批")
	}
}

// TestIsDangerousCommandCustomPatterns 验证配置黑名单可作为内置规则的补充。
func TestIsDangerousCommandCustomPatterns(t *testing.T) {
	h := NewApprovalManager([]string{"kubectl\\s+delete"})
	args, _ := json.Marshal(map[string]string{"command": "kubectl delete pod x"})
	if !h.IsDangerousCommand("bash_tool", string(args)) {
		t.Fatal("配置黑名单应生效")
	}
}
