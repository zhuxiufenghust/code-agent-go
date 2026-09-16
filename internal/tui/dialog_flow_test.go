package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// 构造一个带空事件流的 model，便于直接驱动 handleEvent。
func newTestModel() tuiModel {
	m := New("/tmp", "test-model", nil, nil)
	m.eventCh = make(chan engine.Event)
	return m
}

func approvalEvent(id string, ch chan schema.ApprovalResult) engine.Event {
	return engine.Event{Type: engine.EventApprovalRequired, Data: schema.ApprovalRequest{
		TaskID:        id,
		ToolCall:      schema.ToolCall{Name: "bash_tool", ID: id},
		Reason:        "命中高危黑名单",
		ResultChannel: ch,
	}}
}

// TestApprovalDialogKeysReachDialog 验证弹窗打开时按键会交给对话框处理，
// 并能把选择回传给等待中的引擎，随后事件循环恢复读取。
func TestApprovalDialogKeysReachDialog(t *testing.T) {
	m := newTestModel()
	ch := make(chan schema.ApprovalResult, 1)

	updated, cmd := m.handleEvent(approvalEvent("task-1", ch))
	m = updated.(tuiModel)
	if !m.dialogOpen() {
		t.Fatal("收到审批事件后应弹出对话框")
	}
	if cmd != nil {
		t.Fatal("弹窗期间不应再下发读取下一个事件的 cmd（事件循环需暂停）")
	}

	// 右方向键应切换对话框选中项（若按键被 textarea 吃掉则不会发生）
	d, _ := m.currentDialog().Update(tea.KeyMsg{Type: tea.KeyRight})
	m.dialogs[0] = d
	if m.currentDialog().SelectedIndex != 1 {
		t.Fatalf("右方向键应把选中项切到 1（拒绝）, 实际 %d", m.currentDialog().SelectedIndex)
	}

	// Enter 应由对话框产出 SelectedMsg
	_, dCmd := m.currentDialog().Update(tea.KeyMsg{Type: tea.KeyEnter})
	if dCmd == nil {
		t.Fatal("Enter 应产出 SelectedMsg cmd")
	}
	sel := dCmd().(SelectedMsg)
	if sel.Accepted {
		t.Fatal("选中拒绝时 SelectedMsg.Accepted 应为 false")
	}

	updated, cmd = m.Update(sel)
	m = updated.(tuiModel)
	if m.dialogOpen() {
		t.Fatal("解析后应关闭对话框")
	}
	if cmd == nil {
		t.Fatal("解析后应恢复事件循环读取")
	}
	select {
	case res := <-ch:
		if res.Allowed {
			t.Fatal("用户选择拒绝时 Allowed 应为 false")
		}
	case <-time.After(time.Second):
		t.Fatal("审批结果未回传给引擎")
	}

	// 重复解析不应再向已关闭的 channel 发送（否则 panic）
	m = m.resolveApproval("task-1", true, "重复")
}

// TestApprovalDialogEscCancels 验证 Esc 取消当前审批：按拒绝回传并恢复事件循环。
func TestApprovalDialogEscCancels(t *testing.T) {
	m := newTestModel()
	ch := make(chan schema.ApprovalResult, 1)
	updated, _ := m.handleEvent(approvalEvent("task-2", ch))
	m = updated.(tuiModel)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.dialogOpen() {
		t.Fatal("Esc 后应关闭对话框")
	}
	if cmd == nil {
		t.Fatal("Esc 后应恢复事件循环读取")
	}
	if res := <-ch; res.Allowed || res.Reason != "用户取消" {
		t.Fatalf("Esc 应按取消回传, 实际 %+v", res)
	}
	if !m.textarea.Focused() {
		t.Fatal("关闭弹窗后输入框应恢复聚焦")
	}
}

// TestConcurrentApprovalsQueued 验证并发的多个审批请求会排队而不是互相覆盖，
// 且按 ID 精确回传：解析第二个时不能误伤第一个。
func TestConcurrentApprovalsQueued(t *testing.T) {
	m := newTestModel()
	ch1 := make(chan schema.ApprovalResult, 1)
	ch2 := make(chan schema.ApprovalResult, 1)

	updated, _ := m.handleEvent(approvalEvent("task-a", ch1))
	m = updated.(tuiModel)
	updated, _ = m.handleEvent(approvalEvent("task-b", ch2))
	m = updated.(tuiModel)
	if len(m.dialogs) != 2 {
		t.Fatalf("两个审批请求应同时排队, 实际 %d", len(m.dialogs))
	}
	// 视图应提示还有多少项待审批
	if got := m.dialogView(); !contains(got, "还有 1 项待审批") {
		t.Fatalf("弹窗应提示剩余待审批数量, 实际: %s", got)
	}

	// 批准第二条：只有 task-b 收到结果，task-a 仍在等待
	updated, _ = m.Update(SelectedMsg{Accepted: true, ID: "task-b"})
	m = updated.(tuiModel)
	select {
	case res := <-ch2:
		if !res.Allowed {
			t.Fatal("task-b 应收到批准")
		}
	case <-time.After(time.Second):
		t.Fatal("task-b 未收到结果")
	}
	select {
	case <-ch1:
		t.Fatal("解析 task-b 不应影响仍在等待的 task-a")
	case <-time.After(50 * time.Millisecond):
	}
	if len(m.dialogs) != 1 || m.currentDialog().ID != "task-a" {
		t.Fatalf("应剩下 task-a 在队首, 实际 %+v", m.dialogs)
	}
	if m.textarea.Focused() {
		t.Fatal("仍有待审批时不应把焦点还给输入框")
	}

	// 批准第一条后队列清空并归还焦点
	updated, _ = m.Update(SelectedMsg{Accepted: true, ID: "task-a"})
	m = updated.(tuiModel)
	if m.dialogOpen() || !m.textarea.Focused() {
		t.Fatal("全部解析后应关闭弹窗并归还焦点")
	}
	if res := <-ch1; !res.Allowed {
		t.Fatal("task-a 应收到批准")
	}
}

// TestPendingApprovalsRejectedOnDone 验证运行结束时未决审批会被全部按拒绝回传，
// 避免工具 goroutine 悬挂到审批超时。
func TestPendingApprovalsRejectedOnDone(t *testing.T) {
	m := newTestModel()
	ch1 := make(chan schema.ApprovalResult, 1)
	ch2 := make(chan schema.ApprovalResult, 1)
	updated, _ := m.handleEvent(approvalEvent("task-x", ch1))
	m = updated.(tuiModel)
	updated, _ = m.handleEvent(approvalEvent("task-y", ch2))
	m = updated.(tuiModel)

	updated, _ = m.handleEvent(engine.Event{Type: engine.EventDone})
	m = updated.(tuiModel)
	if m.dialogOpen() {
		t.Fatal("运行结束后不应还有待审批弹窗")
	}
	for i, ch := range []chan schema.ApprovalResult{ch1, ch2} {
		select {
		case res := <-ch:
			if res.Allowed {
				t.Fatalf("第 %d 个未决审批应按拒绝回传", i+1)
			}
		case <-time.After(time.Second):
			t.Fatalf("第 %d 个未决审批未收到回传", i+1)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
