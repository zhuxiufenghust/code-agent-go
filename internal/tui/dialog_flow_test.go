package tui

import (
	"strings"
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
	// 视图应提示当前是第几项、共几项待审批
	if got := m.dialogView(); !contains(got, "第 1/2 项待审批") {
		t.Fatalf("弹窗应提示审批进度, 实际: %s", got)
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

// TestApprovalQueueFromBlockingEventChannel 端到端验证并发审批的真实时序：
// 引擎侧第二个 sendEvent 会阻塞（无人读取），TUI 解析第一条后恢复读取，
// 第二条请求必须接着弹出，而不是被吞掉。
func TestApprovalQueueFromBlockingEventChannel(t *testing.T) {
	m := newTestModel()
	ch := make(chan engine.Event) // 无缓冲：模拟引擎侧阻塞发送
	m.eventCh = ch
	ch1 := make(chan schema.ApprovalResult, 1)
	ch2 := make(chan schema.ApprovalResult, 1)
	go func() {
		ch <- approvalEvent("task-1", ch1)
		ch <- approvalEvent("task-2", ch2) // 阻塞到 TUI 恢复读取
	}()

	updated, cmd := m.Update(readNextEvent(ch)())
	m = updated.(tuiModel)
	if !m.dialogOpen() || m.currentDialog().ID != "task-1" {
		t.Fatal("应弹出第一条审批")
	}
	if cmd != nil {
		t.Fatal("弹窗期间应暂停事件循环")
	}

	// 批准第一条后事件循环恢复，第二条应紧接着弹出
	updated, cmd = m.Update(SelectedMsg{Accepted: true, ID: "task-1"})
	m = updated.(tuiModel)
	if cmd == nil {
		t.Fatal("解析后应恢复事件循环读取")
	}
	if res := <-ch1; !res.Allowed {
		t.Fatal("task-1 应收到批准")
	}
	updated, _ = m.Update(cmd())
	m = updated.(tuiModel)
	if len(m.dialogs) != 1 || m.currentDialog().ID != "task-2" {
		t.Fatalf("第二条审批应继续弹出, 实际队列: %+v", m.dialogs)
	}
	m = m.resolveApproval("task-2", true, "用户批准")
	if res := <-ch2; !res.Allowed {
		t.Fatal("task-2 应收到批准")
	}
	if m.dialogOpen() {
		t.Fatal("全部解析后队列应清空")
	}
}

// dialogBoxLeft 返回实际渲染结果中弹窗边框最左列的位置，用于验证"真的错位了"，
// 而不是只比较样式对象是否不同。
func dialogBoxLeft(m tuiModel) int {
	m.width, m.height = 120, 30
	for _, line := range strings.Split(m.View(), "\n") {
		if i := strings.Index(line, "╭"); i >= 0 {
			return i
		}
	}
	return -1
}

// TestApprovalDialogShiftsPerItem 验证本轮内每处理一条审批，下一条弹窗都会在屏幕上错位，
// 让用户明确感知"换了一条"，而不是以为上一条没提交成功、界面卡住。
func TestApprovalDialogShiftsPerItem(t *testing.T) {
	m := newTestModel()
	ch1 := make(chan schema.ApprovalResult, 1)
	ch2 := make(chan schema.ApprovalResult, 1)
	updated, _ := m.handleEvent(approvalEvent("task-a", ch1))
	m = updated.(tuiModel)

	firstLeft := dialogBoxLeft(m)
	if firstLeft < 0 {
		t.Fatal("未渲染出弹窗边框")
	}

	// 真实时序：第一条解析后事件循环恢复读取，第二条请求才送达
	m = m.resolveApproval("task-a", true, "用户批准")
	updated, _ = m.handleEvent(approvalEvent("task-b", ch2))
	m = updated.(tuiModel)

	secondLeft := dialogBoxLeft(m)
	t.Logf("弹窗左边距: 第 1 项=%d, 第 2 项=%d", firstLeft, secondLeft)
	if secondLeft <= firstLeft {
		t.Fatalf("第二条弹窗应向右错位, 第一条左边距 %d, 第二条 %d", firstLeft, secondLeft)
	}
	if m.currentDialogOrdinal() != 1 {
		t.Fatalf("当前应是第 2 项(序号 1), 实际 %d", m.currentDialogOrdinal())
	}
	if got := m.dialogView(); !contains(got, "第 2 项审批") {
		t.Fatalf("第二条应提示这是第 2 项, 实际: %s", got)
	}

	// 本轮结束（EventDone）后进度归零，下一轮重新从初始位置开始
	updated, _ = m.handleEvent(engine.Event{Type: engine.EventDone})
	m = updated.(tuiModel)
	if m.approvalDone != 0 || m.approvalSeq != 0 {
		t.Fatalf("运行结束后审批进度应归零, 实际 seq=%d done=%d", m.approvalSeq, m.approvalDone)
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
