package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/zhuxiufenghust/code-agent-go/internal/tui"
)

type model struct {
	textarea   textarea.Model
	viewport   viewport.Model
	dialog     tui.ConfirmDialogModel
	showDialog bool
	width      int
	height     int
}

func initialModel() model {
	ta := textarea.New()
	ta.Placeholder = "在这里输入文字,输入 'dialog' 触发审批弹窗…"
	ta.Focus()

	vp := viewport.New(80, 20)
	vp.SetContent("结果将显示在这里。\n")

	return model{
		textarea: ta,
		viewport: vp,
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textarea.SetWidth(msg.Width)
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height/2 - 4
		return m, nil

	case tui.SelectedMsg:
		// 弹窗选择完成:关闭弹窗,把结果写进 viewport,清空输入框
		m.showDialog = false
		result := "批准"
		if msg.Rejected {
			result = "拒绝"
		}
		m.viewport.SetContent(m.viewport.View() + fmt.Sprintf("\n选择结果: %s (ID: %s)", result, msg.ID))
		m.viewport.GotoBottom()
		m.textarea.SetValue("")
		m.textarea.Focus()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEsc {
			return m, tea.Quit
		}
	}

	// 弹窗打开时,所有按键路由给 dialog
	if m.showDialog {
		dm, cmd := m.dialog.Update(msg)
		m.dialog = dm
		return m, cmd
	}

	// 输入模式:路由给 textarea,检测 'dialog' 关键字触发弹窗
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	if strings.Contains(m.textarea.Value(), "dialog") && !m.showDialog {
		m.showDialog = true
		m.dialog = tui.NewConfirmDialogModel("dialog-1", "确认执行该操作?", nil)
	}

	return m, cmd
}

func (m model) View() string {
	if m.showDialog {
		// 弹窗模式下居中显示 dialog
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.dialog.View())
	}
	ui := lipgloss.JoinVertical(
		lipgloss.Left,
		"输入 'dialog' 触发审批弹窗:",
		m.textarea.View(),
		"--- 选择结果 ---",
		m.viewport.View(),
	)
	return ui
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		panic(err)
	}
}
