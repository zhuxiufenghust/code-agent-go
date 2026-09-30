package tui

import (
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// TestUsageEventUpdatesStatus 验证用量事件会刷新状态栏文本。
func TestUsageEventUpdatesStatus(t *testing.T) {
	m := newTestModel()
	if !strings.Contains(m.statusText(), "Token: in=0 out=0") {
		t.Fatalf("初始状态栏应包含 token 用量, 实际: %s", m.statusText())
	}

	updated, _ := m.handleEvent(engine.Event{Type: engine.EventUsage, Data: &schema.Usage{InputTokens: 11, OutputTokens: 22}})
	m = updated.(tuiModel)
	if !strings.Contains(m.statusText(), "Token: in=11 out=22") {
		t.Fatalf("用量事件后状态栏应更新, 实际: %s", m.statusText())
	}
}

// TestUsageEventKeepsEventLoopAlive 是核心回归：
// 用量事件必须继续读取下一个事件。若在这里中断，后续事件无人消费、
// 引擎阻塞在 sendEvent，EventDone 永不到达，输入框也就再也拿不回焦点。
func TestUsageEventKeepsEventLoopAlive(t *testing.T) {
	m := newTestModel()
	m.eventCh = make(chan engine.Event)
	updated, cmd := m.handleEvent(engine.Event{Type: engine.EventUsage, Data: &schema.Usage{InputTokens: 1, OutputTokens: 2}})
	m = updated.(tuiModel)
	if cmd == nil {
		t.Fatal("用量事件后必须返回读取下一个事件的 cmd，否则事件循环会断在这里")
	}
}

// TestUsageEventDoesNotBreakActionStream 验证用量事件不会把正在流式的正文切断。
func TestUsageEventDoesNotBreakActionStream(t *testing.T) {
	m := newTestModel()
	updated, _ := m.handleEvent(engine.Event{Type: engine.EventActionDelta, Data: "正在流式输出"})
	m = updated.(tuiModel)
	before := m.pendingAction

	updated, _ = m.handleEvent(engine.Event{Type: engine.EventUsage, Data: &schema.Usage{InputTokens: 5, OutputTokens: 6}})
	m = updated.(tuiModel)
	if m.pendingAction != before {
		t.Fatalf("用量事件不应结束正文块, 期望 pendingAction=%q, 实际 %q", before, m.pendingAction)
	}

	// 后续增量应继续拼在同一块里
	updated, _ = m.handleEvent(engine.Event{Type: engine.EventActionDelta, Data: "的正文"})
	m = updated.(tuiModel)
	if !strings.Contains(strings.Join(m.lines, ""), "正在流式输出的正文") {
		t.Fatalf("正文应连续累积, 实际: %v", m.lines)
	}
}

// TestUsageEventToleratesBadData 验证异常载荷不会 panic（nil / 值类型 / 类型不符）。
func TestUsageEventToleratesBadData(t *testing.T) {
	m := newTestModel()
	m.eventCh = make(chan engine.Event)

	// 契约：引擎固定发送 *schema.Usage；其余形态都属异常，忽略并打日志，但事件循环必须继续。
	for name, evt := range map[string]engine.Event{
		"nil 指针": {Type: engine.EventUsage, Data: (*schema.Usage)(nil)},
		"值类型":    {Type: engine.EventUsage, Data: schema.Usage{InputTokens: 3, OutputTokens: 4}},
		"类型不符":   {Type: engine.EventUsage, Data: "oops"},
		"空载荷":    {Type: engine.EventUsage},
	} {
		updated, cmd := m.handleEvent(evt)
		if cmd == nil {
			t.Fatalf("%s: 异常载荷后仍应继续读取下一个事件", name)
		}
		got := updated.(tuiModel)
		if got.usage.InputTokens != 0 || got.usage.OutputTokens != 0 {
			t.Fatalf("%s: 异常载荷不应被计入用量, 实际 %+v", name, got.usage)
		}
	}

	// 正常载荷仍然生效
	updated, _ := m.handleEvent(engine.Event{Type: engine.EventUsage, Data: &schema.Usage{InputTokens: 3, OutputTokens: 4}})
	m = updated.(tuiModel)
	if !strings.Contains(m.statusText(), "Token: in=3 out=4") {
		t.Fatalf("指针载荷应被采用, 实际: %s", m.statusText())
	}
}
