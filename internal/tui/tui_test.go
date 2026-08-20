package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestInputReceivesKeystrokes 验证构造后 textarea 处于聚焦状态，
// 普通字符按键能正确累加到 textarea.Value()。
func TestInputReceivesKeystrokes(t *testing.T) {
	m := New("/tmp", "test-model", nil)

	// 模拟依次键入 "hello"
	for _, r := range "hello" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ := m.Update(key)
		m = updated.(tuiModel)
	}

	if got := m.textarea.Value(); got != "hello" {
		t.Fatalf("期望 textarea 累计为 'hello', 实际 %q", got)
	}

	// 回车后应清空，并回显到 viewport 内容；且保持聚焦、光标回到第一行。
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	updated, _ := m.Update(enter)
	m = updated.(tuiModel)

	if got := m.textarea.Value(); got != "" {
		t.Fatalf("回车后 textarea 应被清空, 实际 %q", got)
	}
	if len(m.lines) == 0 {
		t.Fatal("回车后 viewport 内容应追加用户输入")
	}
	if !m.textarea.Focused() {
		t.Fatal("回车后 textarea 应保持聚焦")
	}
}

// TestInitModelIsFocused 验证 New 构造的 model 其 textarea 已聚焦。
func TestInitModelIsFocused(t *testing.T) {
	m := New("/tmp", "test-model", nil)
	if !m.textarea.Focused() {
		t.Fatal("构造后的 textarea 应处于聚焦状态, 否则无法接收按键")
	}
}

// TestAltEnterInsertsNewline 验证 Alt+Enter 能插入换行（多行输入）。
func TestAltEnterInsertsNewline(t *testing.T) {
	m := New("/tmp", "test-model", nil)
	// 输入 "hi"
	for _, r := range "hi" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ := m.Update(key)
		m = updated.(tuiModel)
	}
	// Alt+Enter 换行
	altEnter := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	updated, _ := m.Update(altEnter)
	m = updated.(tuiModel)
	// 再输入 "yo"
	for _, r := range "yo" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ = m.Update(key)
		m = updated.(tuiModel)
	}

	if got := m.textarea.Value(); got != "hi\nyo" {
		t.Fatalf("期望 Alt+Enter 插入换行得到 'hi\\nyo', 实际 %q", got)
	}
}

// TestCtrlJInsertsNewline 验证 Ctrl+J 能插入换行（终端可靠发送的兜底换行键）。
func TestCtrlJInsertsNewline(t *testing.T) {
	m := New("/tmp", "test-model", nil)
	for _, r := range "hi" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ := m.Update(key)
		m = updated.(tuiModel)
	}
	ctrlJ := tea.KeyMsg{Type: tea.KeyCtrlJ}
	updated, _ := m.Update(ctrlJ)
	m = updated.(tuiModel)
	for _, r := range "yo" {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		updated, _ = m.Update(key)
		m = updated.(tuiModel)
	}

	if got := m.textarea.Value(); got != "hi\nyo" {
		t.Fatalf("期望 Ctrl+J 插入换行得到 'hi\\nyo', 实际 %q", got)
	}
}
