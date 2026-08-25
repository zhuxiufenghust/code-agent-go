package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
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

	thinkingHeaderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238")).
				Italic(true)
	thinkingLineStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("238"))

	thinkingEndStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("236"))

	toolStartStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffaf5f"))
	toolResultStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ff5f5f"))
)

// eventMsg 将 engine.Event 包装为 tea.Msg，供 Bubbletea 的 Update 分发。
type eventMsg engine.Event

type tuiModel struct {
	// 展示配置（构造时设置，后续不变）
	workDir   string
	modelName string
	agent     *engine.AgentEngine
	eventCh   <-chan engine.Event // agent 流式运行的事件 channel
	cancel    context.CancelFunc  // 流式运行所属的 context 取消函数，结束时(或退出)调用以防泄漏

	// Thinking 块流式状态：
	// pendingThinking 累积当前轮次的推理文本；thinkingLineStart 记录 « thinking » 标题行在 lines 中的索引。
	// thinkingLineStart == -1 表示本轮尚未开始 thinking 块。
	pendingThinking   string
	thinkingLineStart int

	// Action（正文）块流式状态，与 thinking 对称：
	// pendingAction 累积当前轮次的正文增量；actionLineStart 记录正文块首行在 lines 中的索引。
	// actionLineStart == -1 表示当前轮尚未开始正文块。
	// 每收到一个增量 delta 就把它“拼接”进 pendingAction 并立即渲染，使终端收到多少就显示多少：
	// 网关若逐 token 流式则在终端逐字出现；若整块到达则整块拼接，不做任何延迟/动画。
	pendingAction   string
	actionLineStart int

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

func New(workDir string, modelName string, agent *engine.AgentEngine) tuiModel {

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
		workDir:           workDir,
		modelName:         modelName,
		agent:             agent,
		textarea:          ta,
		viewport:          vp,
		status:            status,
		thinkingLineStart: -1,
		actionLineStart:   -1,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

// wrapByColumns 按“显示列宽”折行：终端/ viewport 以列计宽，中文等宽字符占 2 列，
// 因此必须按 ansi.StringWidth 累计列宽，而不是按 rune 数。否则 CJK 长行会被 viewport 的
// MaxWidth 截断、丢内容。优先在空格处断行；若某个无空格的连续片段（中文长句/URL）本身超过
// maxCols，则按列硬断（不拆散单个字形）。
func wrapByColumns(text string, maxCols int) []string {
	if maxCols <= 0 {
		return []string{text}
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapParagraphByColumns(para, maxCols)...)
	}
	return out
}

func wrapParagraphByColumns(para string, maxCols int) []string {
	words := strings.Fields(para)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	cur := ""
	curW := 0
	flush := func() {
		lines = append(lines, cur)
		cur = ""
		curW = 0
	}
	// 硬断一个超过行宽的“单词”（中文长句/URL 等无空格片段）。
	hardBreak := func(word string) {
		for _, r := range word {
			rw := ansi.StringWidth(string(r))
			if curW+rw > maxCols && curW > 0 {
				flush()
			}
			cur += string(r)
			curW += rw
		}
	}

	for _, w := range words {
		wW := ansi.StringWidth(w)
		if cur == "" {
			if wW > maxCols {
				hardBreak(w)
			} else {
				cur = w
				curW = wW
			}
			continue
		}
		if curW+1+wW <= maxCols {
			cur += " " + w
			curW += 1 + wW
		} else {
			flush()
			if wW > maxCols {
				hardBreak(w)
			} else {
				cur = w
				curW = wW
			}
		}
	}
	flush()
	return lines
}

// renderThinkingLines 将思考文本按显示列宽折行，并加上 "  │ " 前缀与灰色样式。
// 列宽预算 = 终端列宽 - 前缀列宽(4) - 右侧边距(1)，保证每行总列宽不超过终端，
// 从而 viewport 的 MaxWidth 不会截断思考内容。
func renderThinkingLines(text string, width int) []string {
	const prefix = "  │ "
	const prefixCols = 4 // "  │ " 占用的显示列数

	contentCols := width - prefixCols - 1 // 右侧留 1 列边距
	if contentCols < 20 {
		// 终端过窄时不折行，单行输出。
		return []string{thinkingLineStyle.Render(prefix + text)}
	}

	var out []string
	for _, para := range strings.Split(text, "\n") {
		for _, line := range wrapByColumns(para, contentCols) {
			out = append(out, thinkingLineStyle.Render(prefix+line))
		}
	}
	if len(out) == 0 {
		out = []string{thinkingLineStyle.Render(prefix)}
	}
	return out
}

// renderViewportLines 把 m.lines 中每一行按 viewport 列宽预折行后写入 viewport，
// 保证没有任何一行显示列宽超过 viewport 宽度——这样 viewport 的 MaxWidth 永远无需截断，
// 中英文（含 CJK 占 2 列）长行都能完整显示，而不是被裁掉尾部。
// 注意：使用值接收者时必须在调用处接收返回值（m = m.renderViewportLines()），
// 否则对 m.viewport 的修改会随值拷贝被丢弃，viewport 内容始终为空。
// 已按列宽折行好的思考行（带前缀/样式）本身不超过宽度，直接透传，避免重折破坏其前缀与 ANSI 样式。
func (m tuiModel) renderViewportLines() tuiModel {
	maxCols := m.viewport.Width
	if maxCols <= 0 {
		maxCols = 80 // 尚未收到 WindowSizeMsg 时的兜底宽度
	}
	wrapped := make([]string, 0, len(m.lines))
	for _, l := range m.lines {
		if ansi.StringWidth(l) <= maxCols {
			wrapped = append(wrapped, l)
			continue
		}
		for _, wl := range wrapByColumns(l, maxCols) {
			wrapped = append(wrapped, wl)
		}
	}
	m.viewport.SetContent(strings.Join(wrapped, "\n"))
	m.viewport.GotoBottom()
	return m
}

func readNextEvent(ch <-chan engine.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return eventMsg{Type: engine.EventDone}
		}
		return eventMsg(evt)
	}
}

func (m tuiModel) flushPendingThinking() tuiModel {
	if m.pendingThinking == "" {
		return m
	}
	m.lines = append(m.lines, thinkingEndStyle.Render("  └ ──────────────────────────────"))
	m.pendingThinking = ""
	m.thinkingLineStart = -1

	return m
}

// finalizePendingAction 把当前轮次已累积的正文块定稿：文本已写入 m.lines，
// 这里只是重置累加缓冲与起始索引，使下一轮（或工具调用后）的正文从新的一行开始累积。
func (m tuiModel) finalizePendingAction() tuiModel {
	m.pendingAction = ""
	m.actionLineStart = -1
	return m
}

func (m tuiModel) handleEvent(evt engine.Event) (tea.Model, tea.Cmd) {

	switch evt.Type {
	case engine.EventThinkingDelta:
		delta, _ := evt.Data.(string)
		//log.Info("handle_event", zap.String("data", delta), zap.Any("type", evt.Type))
		// thinking 块流式输出：累积到 pendingThinking，等本轮结束后再追加到 lines。
		if m.thinkingLineStart == -1 {
			m.lines = append(m.lines, thinkingHeaderStyle.Render("« thinking »"))
			m.thinkingLineStart = len(m.lines) - 1
		}
		m.pendingThinking += delta
		thinkingLines := renderThinkingLines(m.pendingThinking, m.width)
		m.lines = append(m.lines[:m.thinkingLineStart+1], thinkingLines...)

	case engine.EventActionDelta:
		// 正文流式累积：把增量“拼接”进同一正文块并立即渲染，收到多少显示多少。
		// 网关逐 token 流式则终端逐字出现；整块到达则整块拼接，不做任何延迟或动画。
		delta, _ := evt.Data.(string)
		//log.Info("handle_event", zap.String("data", delta), zap.Any("type", evt.Type))
		if m.actionLineStart == -1 {
			m.lines = append(m.lines, "")
			m.actionLineStart = len(m.lines) - 1
		}
		m.pendingAction += delta
		wrapped := wrapByColumns(m.pendingAction, m.width)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		// 用累积正文（按列宽折行后）原地替换从 actionLineStart 开始的所有行。
		m.lines = append(m.lines[:m.actionLineStart], wrapped...)

	case engine.EventToolStart:
		// 工具调用前先把已累积的正文定稿，避免与工具行混在同一块。
		m = m.finalizePendingAction()
		tc, _ := evt.Data.(schema.ToolCall)
		m.lines = append(m.lines, toolStartStyle.Render(fmt.Sprintf("⚙ 调用工具: %s", tc.Name)))

	case engine.EventToolResult:
		m = m.finalizePendingAction()
		td, _ := evt.Data.(engine.ToolResultData)
		out := td.Result.Output
		if td.Result.IsError {
			out = "错误: " + out
		}
		m.lines = append(m.lines, toolResultStyle.Render(fmt.Sprintf("  └─ %s (%.0fms)", out, float64(td.Duration.Milliseconds()))))

	case engine.EventError:
		m = m.finalizePendingAction()
		errMsg, _ := evt.Data.(string)
		m.lines = append(m.lines, errorStyle.Render("运行错误: "+errMsg))
		// 出错也视为本轮结束，取消流式 context，避免 goroutine/连接泄漏。
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}

	case engine.EventDone:
		// channel 已关闭（或 agent 显式 done）：收尾，停止事件循环，恢复输入聚焦。
		if m.pendingThinking != "" {
			m = m.flushPendingThinking()
		}
		m = m.finalizePendingAction()
		// 流式运行结束，取消其 context，释放底层连接与 goroutine。
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m = m.renderViewportLines()
		m.textarea.Focus()
		return m, nil
	}

	// 除终止事件外，每个事件处理完都要继续读取下一个，否则事件循环会在
	// 第一个非 thinking 事件(如首段 action_delta)后停住，永远到不了最后一行。
	m = m.renderViewportLines()
	return m, readNextEvent(m.eventCh)
}

func (m tuiModel) streamRun(userPrompt string) (tea.Model, tea.Cmd) {
	// 启动 agent 流式运行，返回一个 channel 用于接收 engine.Event。
	// 注意：StreamRun 会立即返回（内部在 goroutine 中跑 agent loop），
	// 因此这里不能 defer cancel()——否则 context 会在函数返回瞬间被取消，
	// 导致 provider 的 sendStreamChunk/done 全部因 ctx.Done() 而丢弃，
	// 最终报 “provider stream ended without done chunk”。
	// cancel 保存在 model 上，待 EventDone / EventError / 退出时再调用。
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	m.textarea.Blur()

	ch, err := m.agent.StreamRun(ctx, userPrompt)
	if err != nil {
		cancel()
		m.cancel = nil
		m.lines = append(m.lines, "启动流式运行失败: "+err.Error())
		m = m.renderViewportLines()
		m.textarea.Focus()
		return m, textarea.Blink
	}
	log.Debug("tui_stream_run", zap.String("user_prompt", userPrompt))

	m.eventCh = ch
	return m, readNextEvent(ch)
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
	case eventMsg:
		return m.handleEvent(engine.Event(msg))

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			// 退出前取消流式 context，释放底层连接与 goroutine。
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
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
				m = m.renderViewportLines()
			}
			m.textarea.SetValue("")
			m.textarea.Focus() // 保持聚焦，光标回到第一行
			// 必须接收 streamRun 返回的 model(含 eventCh) 与 cmd(readNextEvent)，
			// 否则事件循环不会被启动，最终日志/EventDone 不会被处理。
			m, cmd := m.streamRun(value)
			return m, cmd
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
