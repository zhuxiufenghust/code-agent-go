package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
)

func u2(m tuiModel, msg tea.Msg) tuiModel {
	mm, _ := m.Update(msg)
	return mm.(tuiModel)
}

func TestViewportHeightAccountsForBox(t *testing.T) {
	m := New("/tmp", "m")
	termH := 30
	m = u2(m, tea.WindowSizeMsg{Width: 100, Height: termH})

	boxH := lipgloss.Height(inputBoxStyle.Render(m.textarea.View()))
	total := m.viewport.Height + boxH + 1
	if total > termH {
		t.Fatalf("布局高度 %d 超出终端高度 %d（viewport=%d box=%d）",
			total, termH, m.viewport.Height, boxH)
	}
	if m.viewport.Height <= 0 {
		t.Fatalf("viewport 高度应为正, 实际 %d", m.viewport.Height)
	}
}
