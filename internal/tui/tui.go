package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// inputBoxStyle 是输入框外框样式，Update 与 View 共用，
// 以便按真实渲染高度动态计算 viewport 可用高度。
var (
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(0, 1)
	statusBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Background(lipgloss.Color("#020202")).
			Foreground(lipgloss.Color("#8df456")).
			Padding(0, 1)
)

type tuiModel struct {
	// 展示配置（构造时设置，后续不变）
	workDir   string
	modelName string

	viewport viewport.Model // 输出历史展示
	width    int
	height   int
	// lines 保存已发送内容的各行，作为向 viewport 追加的缓冲。
	// 仅保留最近 maxViewportLines 行，避免 content 无限增长导致内存与重渲染开销膨胀。
	lines []string

	textarea textarea.Model // 输入框

	status LineText // workDir + model + token ratio + status
}

// maxViewportLines 是 viewport 历史保留的最大行数，超出部分丢弃最旧的。
const maxViewportLines = 2000

// statusInnerWidth 去掉状态栏外框（border + 左右 padding 各 1）占用的宽度，
// 使内部文本宽度不超过终端，避免被裁切而出现“内容为空”的假象。
func statusInnerWidth(w int) int {
	const decoration = 4
	if w <= decoration {
		return 1
	}
	return w - decoration
}

func New(workDir string, modelName string) tuiModel {

	ta := textarea.New()
	ta.Placeholder = "输入任务, 按 Enter 发送, Alt+Enter / Ctrl+J 换行..."
	ta.SetWidth(60)
	ta.SetHeight(5) // ✅ 设置显示高度（行数）
	ta.CharLimit = 1000
	ta.ShowLineNumbers = true // 是否显示行号
	// 重新绑定换行键：默认 Enter 会插入换行，会与“Enter 发送”冲突。
	// 普通 Enter 仅用于发送（在 Update 中拦截，不传给 textarea）。
	// 换行用 Alt+Enter 或 Ctrl+J：Alt+Enter 在多数终端会被拆成 Esc+Enter 而失效，
	// 因此额外绑定 Ctrl+J（真实控制字符，终端可靠发送）作为兜底。
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	// 必须在构造时聚焦：Init 只返回 tea.Cmd 而不返回 model，
	// 在 Init 中调用 Focus() 对副本的修改会被 bubbletea 丢弃，
	// 导致 textarea 始终未聚焦、无法接收按键输入。
	ta.Focus()

	vp := viewport.New(80, 20)

	status := NewLineText("工作目录: " + workDir + " | 模型: " + modelName + " ")

	return tuiModel{
		workDir:   workDir,
		modelName: modelName,

		textarea: ta,
		viewport: vp,
		status:   status,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}
func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.status = m.status.WithWidth(statusInnerWidth(msg.Width))
		m.viewport.Width = msg.Width
		// viewport 高度 = 终端高度 - 底栏(输入框+状态栏)真实渲染高度。
		// 通过 footerHeight() 从实际渲染结果测量，新增底栏组件时无需手动改这里，
		// 否则少减高度会让整体布局超出终端、顶部的历史消息被裁掉。
		m.viewport.Height = msg.Height - m.footerHeight()
		if m.viewport.Height < 1 {
			m.viewport.Height = 1
		}
	case SetTextMsg:
		sm, _ := m.status.Update(msg)
		m.status = sm.(LineText)

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			// 发送：读取内容并回显到 viewport，然后清空。
			// 注意：不再把 enter 传给 textarea.Update，避免默认插入换行导致光标跳到第二行。
			value := m.textarea.Value()
			if value != "" {
				// 按行追加到缓冲，并裁剪到最近的 maxViewportLines 行（环形上限）。
				parts := strings.Split(value, "\n")
				m.lines = append(m.lines, parts...)
				if len(m.lines) > maxViewportLines {
					m.lines = m.lines[len(m.lines)-maxViewportLines:]
				}
				m.viewport.SetContent(strings.Join(m.lines, "\n"))
				m.viewport.GotoBottom() // 滚动到底部
			}
			m.textarea.SetValue("")
			m.textarea.Focus() // 保持聚焦，光标回到第一行
			return m, nil
		}
	}

	// 关键：委派消息给 textarea，让它处理按键（Alt+Enter 换行等）
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)

	return m, tea.Batch(cmd, vpCmd)
}

// footer 返回除 viewport 外的所有固定底栏（输入框 + 状态栏），
// View 与 footerHeight 共用，保证“渲染”与“测高”完全一致。
func (m tuiModel) footer() string {
	inputBox := inputBoxStyle.Render(m.textarea.View())
	statusBar := statusBoxStyle.Render(m.status.WithWidth(statusInnerWidth(m.width)).View())
	return lipgloss.JoinVertical(lipgloss.Left, inputBox, statusBar)
}

// footerHeight 测量底栏的真实渲染高度，供 viewport 自动分配剩余空间。
func (m tuiModel) footerHeight() int {
	return lipgloss.Height(m.footer())
}

func (m tuiModel) View() string {
	// status 必须是独立底栏，不能当作 inputBoxStyle.Render 的第二个参数：
	// lipgloss 的 Render 会把多余参数用空格拼接到同一边框内，导致状态栏被嵌套进输入框、宽度溢出被裁切而“看不见文字”。
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.viewport.View(),
		m.footer(),
	)
}
