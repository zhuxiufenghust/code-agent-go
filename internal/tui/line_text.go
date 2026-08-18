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
