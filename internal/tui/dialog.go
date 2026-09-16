package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type ConfirmDialogModel struct {
	AcceptedText  string
	RejectedText  string
	Title         string
	SelectedIndex int // 0 for Accepted, 1 for Rejected
	ID            string
	Data          interface{}
}

type SelectedMsg struct {
	Accepted bool
	Rejected bool
	ID       string
}

const AcceptedText = "批准"
const RejectedText = "拒绝"

var (
	dialogWordStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E7E1CC"))

	dialogStyle = dialogWordStyle.
			Width(36).
			Height(6).
			Padding(1, 3).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#874BFD"))
	acceptWordLightColor = lipgloss.Color("#43BF6D")
	denyWordLightColor   = lipgloss.Color("#E74C3C")
)

func NewConfirmDialogModel(id string, title string, data interface{}) ConfirmDialogModel {
	return ConfirmDialogModel{
		Title:         title,
		AcceptedText:  AcceptedText,
		RejectedText:  RejectedText,
		SelectedIndex: 0, // Default to Accepted
		ID:            id,
		Data:          data,
	}
}

func (m ConfirmDialogModel) View() string {
	accepted := dialogWordStyle.Foreground(acceptWordLightColor).Render(m.AcceptedText)
	rejected := dialogWordStyle.Foreground(denyWordLightColor).Render(m.RejectedText)
	if m.SelectedIndex == 0 {
		accepted = dialogWordStyle.Foreground(acceptWordLightColor).Bold(true).Render("▶ " + m.AcceptedText)
		rejected = dialogWordStyle.Faint(true).Render(m.RejectedText)
	} else {
		rejected = dialogWordStyle.Foreground(denyWordLightColor).Bold(true).Render("▶ " + m.RejectedText)
		accepted = dialogWordStyle.Faint(true).Render(m.AcceptedText)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, accepted, "    ", rejected)
	body := lipgloss.JoinVertical(lipgloss.Center, m.Title, buttons)
	return dialogStyle.Render(body)
}
func (m ConfirmDialogModel) Init() tea.Cmd {
	return nil
}

func (m ConfirmDialogModel) Update(msg tea.Msg) (ConfirmDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "left", "h":
			m.SelectedIndex = 0
		case "right", "l":
			m.SelectedIndex = 1
		case "enter":
			// Handle the selection based on SelectedIndex
			if m.SelectedIndex == 0 {
				return m, func() tea.Msg {
					return SelectedMsg{Accepted: true, Rejected: false, ID: m.ID}
				}
			} else {
				return m, func() tea.Msg {
					return SelectedMsg{Accepted: false, Rejected: true, ID: m.ID}
				}
			}

		}
	}
	return m, nil
}
