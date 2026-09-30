package tui

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ===== 单行文本组件 =====
type LineText struct {
	text  string
	width int
	style lipgloss.Style
}

func NewLineText(text string) LineText {
	return LineText{
		text:  text,
		style: lipgloss.NewStyle(),
	}
}

func (c LineText) WithStyle(s lipgloss.Style) LineText {
	c.style = s
	return c
}

// WithText 直接替换文本：用于状态栏这类"数据变了就地重绘"的场景，
// 不必绕一圈 SetTextMsg（少一次事件往返，也就少一处漏掉事件循环收尾的风险）。
func (c LineText) WithText(text string) LineText {
	c.text = text
	return c
}

func (c LineText) WithWidth(w int) LineText {
	c.width = w
	return c
}

func (c LineText) Init() tea.Cmd { return nil }

func (c LineText) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SetTextMsg:
		c.text = msg.Text
	case tea.WindowSizeMsg:
		c.width = msg.Width
	}
	return c, nil
}

func (c LineText) View() string {
	if c.width <= 0 {
		return c.style.Render(c.text)
	}
	truncated := truncateToWidth(c.text, c.width)
	padded := lipgloss.NewStyle().Width(c.width).Render(truncated)
	return c.style.Render(padded)
}

type SetTextMsg struct{ Text string }

func truncateToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	budget := w - 1
	var width int
	var end int
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		rw := lipgloss.Width(string(r))
		if width+rw > budget {
			break
		}
		width += rw
		end += size
	}
	return s[:end] + "…"
}
