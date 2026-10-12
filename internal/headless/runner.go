// Package headless 提供非交互运行入口：一次用户输入进、一份结构化结果出。
//
// 它消费的是引擎的 StreamRun 事件流，与 TUI 同源：
// 差别只在"谁来当那个把 delta 拼成答案、把审批事件变成决策的消费者"。
// 之所以要做成一个独立的可测包而不是塞在 cmd 里，是因为这类逻辑的错误几乎都发生在
// "某些事件组合"（提前结束、错误事件、审批事件、ctx 取消），只有把事件流换成假的
// 才能稳定复现；真跑到 LLM 上排查成本极高。
package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// Streamer 是 headless 运行所需的最小引擎能力。
// 只声明这一个方法，单测就能用"假的事件流"替换掉真实 LLM。
type Streamer interface {
	StreamRun(ctx context.Context, prompt string) (<-chan engine.Event, error)
}

// Format 是无头模式的输出形态。
type Format string

const (
	// FormatText 是给人看的：助手正文写 Out，工具/错误等诊断写 Err，
	// 于是 `app -p "..." > answer.md` 拿到的是纯净答案。
	FormatText Format = "text"
	// FormatJSON 是给脚本看的：事件与最终结果都以单行 JSON 写到 Out，
	// Err 保持空，避免日志污染可解析的流。
	FormatJSON Format = "json"
)

// ParseFormat 把命令行字符串解析成 Format，只接受 text / json 两种。
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(FormatText):
		return FormatText, nil
	case string(FormatJSON):
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("不支持的输出格式 %q（可选 text / json）", s)
	}
}

// String 让 Runner 的配置能直接进日志/%v。
func (f Format) String() string { return string(f) }

const (
	// defaultMaxToolOutput 是文本模式下单个工具结果打印到诊断流的最大字节数。
	// 工具输出本身已在 tools 层截断过（保护上下文），这里是第二道"只保护终端"的截断：
	// 前者必须严格（影响 token 成本），后者可以更宽松。
	defaultMaxToolOutput = 4000
)

// Runner 消费一次流式运行，并按 Format 把结果写到 Out / Err。
type Runner struct {
	// Out 文本模式下只承接助手正文；JSON 模式下承接全部结构化行。
	Out io.Writer
	// Err 文本模式下承接工具/错误/摘要等诊断信息；JSON 模式下不被使用。
	Err io.Writer
	// Format 输出形态，零值按 FormatText 处理。
	Format Format
	// ShowThinking 是否在文本模式下输出模型推理增量（默认关闭：推理过程噪音大）。
	ShowThinking bool
	// MaxToolOutput 工具结果打印上限，<=0 时取 defaultMaxToolOutput。
	MaxToolOutput int
}

// Result 是一次无头运行的聚合结果，也是 JSON 模式下的收尾事件载荷。
type Result struct {
	// Text 是本次运行的助手正文（所有 action delta 的拼接）。
	Text string `json:"text"`
	// Note 是引擎主动收尾时给用户的说明（达到最大轮次 / 判定卡住）。
	Note      string `json:"note,omitempty"`
	ToolCalls int    `json:"tool_calls"`
	// ToolErrors 是返回 IsError 的工具调用数。它与"工具没跑起来"不同：
	// 后者会让整轮以错误事件结束，前者已被写回上下文供模型自愈。
	ToolErrors int `json:"tool_errors"`
	// ApprovalsDenied 是无通道情况下被自动拒绝的高危命令数。
	ApprovalsDenied int `json:"approvals_denied"`
	Compactions     int `json:"compactions"`
	// Turns 是本次运行实际推进的轮次数（LLM 调用轮）。
	Turns int `json:"turns"`
	// Usage 是最后一次上报的累计用量（引擎上报的就是累计值，不是增量）。
	Usage schema.Usage `json:"usage"`
}

// resultEventType 是 JSON 模式下收尾那一行的 type，用于与引擎事件区分
// （引擎事件里没有 result，脚本据此判断"跑完了"）。
const resultEventType = "result"

type resultEvent struct {
	Type string `json:"type"`
	Data Result `json:"data"`
}

// Run 阻塞消费一次流式运行，直到事件结束、出错或 ctx 取消。
//
// 语义约定：
//   - 事件流正常结束（收到 done 或 channel 关闭）时返回 (Result, nil)；
//   - 引擎以错误事件收尾时返回该错误，Result 仍带上已经跑出的部分结果，
//     便于调用方在错误时也能输出已获得的用量/工具统计；
//   - ctx 被取消时返回 ctx.Err()，同样带上部分结果。
func (r *Runner) Run(ctx context.Context, agent Streamer, prompt string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	format := r.Format
	if format == "" {
		format = FormatText
	}
	var res Result

	ch, err := agent.StreamRun(ctx, prompt)
	if err != nil {
		return res, fmt.Errorf("启动流式运行失败: %w", err)
	}

	var runErr error
	for {
		select {
		case <-ctx.Done():
			r.finish(ctx, format, res, ctx.Err())
			return res, ctx.Err()
		case evt, ok := <-ch:
			if !ok {
				// channel 关闭而没有 done 事件：引擎提前退出（例如进程收尾）。
				// 不视为错误——文本内容可能已经完整产出，调用方据此自行判断。
				r.finish(ctx, format, res, nil)
				return res, nil
			}
			if evt.Turn > res.Turns {
				res.Turns = evt.Turn
			}
			switch evt.Type {
			case engine.EventThinkingDelta:
				r.handleThinking(evt, &res)
			case engine.EventActionDelta:
				r.handleAction(evt, format, &res)
			case engine.EventToolStart:
				r.handleToolStart(evt, format, &res)
			case engine.EventToolResult:
				r.handleToolResult(evt, format, &res)
			case engine.EventCompaction:
				res.Compactions++
				r.handleGeneric(evt, format, "compaction")
			case engine.EventUsage:
				if u, ok := evt.Data.(*schema.Usage); ok && u != nil {
					res.Usage = *u
				}
				r.handleGeneric(evt, format, "usage")
			case engine.EventFinal:
				if text, ok := evt.Data.(string); ok {
					res.Note = text
				}
				r.handleGeneric(evt, format, "final")
			case engine.EventApprovalRequired:
				r.handleApproval(ctx, evt, format, &res)
			case engine.EventError:
				if msg, ok := evt.Data.(string); ok {
					runErr = fmt.Errorf("agent 运行失败: %s", msg)
				} else {
					runErr = fmt.Errorf("agent 运行失败: %v", evt.Data)
				}
				// 错误事件之后引擎会关闭 channel，本次收尾由这里负责。
				r.finish(ctx, format, res, runErr)
				return res, runErr
			case engine.EventDone:
				// done 也是一条事件：脚本消费流式输出时用它判断"事件本身跑完了"，
				// result 只是紧随其后的聚合收尾行。
				r.handleGeneric(evt, format, string(engine.EventDone))
				r.finish(ctx, format, res, nil)
				return res, nil
			default:
				r.handleGeneric(evt, format, string(evt.Type))
			}
		}
	}
}

// finish 输出收尾内容：JSON 模式下补一行 result，文本模式下补一段摘要。
func (r *Runner) finish(_ context.Context, format Format, res Result, err error) {
	if format == FormatJSON {
		r.writeJSON(resultEvent{Type: resultEventType, Data: res})
		return
	}
	// 错误本体由调用方打印（带退出码语义），这里只补一行运行统计，
	// 避免同一个错误在同一终端里出现两次。
	r.printErr(summaryLine(res))
}

func (r *Runner) handleThinking(evt engine.Event, _ *Result) {
	text, _ := evt.Data.(string)
	if r.Format == FormatJSON {
		// 机器消费模式下推理过程同样是一条事件：是否展示由下游决定，
		// 这里不能替它丢弃信息。
		r.writeJSONLine(evt)
		return
	}
	if !r.ShowThinking || text == "" {
		return
	}
	r.printErr(text)
}

func (r *Runner) handleAction(evt engine.Event, format Format, res *Result) {
	text, _ := evt.Data.(string)
	if text == "" {
		return
	}
	res.Text += text
	if format == FormatJSON {
		r.writeJSONLine(evt)
		return
	}
	r.printOut(text)
}

func (r *Runner) handleToolStart(evt engine.Event, format Format, res *Result) {
	res.ToolCalls++
	if tc, ok := evt.Data.(schema.ToolCall); ok {
		if format == FormatJSON {
			r.writeJSONLine(evt)
			return
		}
		r.printErr(fmt.Sprintf("→ 调用工具 %s 参数=%s", tc.Name, compactArgs(tc.Arguments)))
		return
	}
	if format == FormatJSON {
		r.writeJSONLine(evt)
	}
}

func (r *Runner) handleToolResult(evt engine.Event, format Format, res *Result) {
	data, ok := evt.Data.(engine.ToolResultData)
	if !ok {
		return
	}
	if data.Result.IsError {
		res.ToolErrors++
	}
	if format == FormatJSON {
		r.writeJSONLine(evt)
		return
	}
	mark := "✓"
	if data.Result.IsError {
		mark = "✗"
	}
	r.printErr(fmt.Sprintf("%s 工具返回 %s 耗时=%s", mark, data.Result.Name,
		data.Duration.Round(time.Millisecond)))
	if out := truncate(data.Result.Output, r.maxToolOutput()); out != "" {
		for _, line := range strings.Split(out, "\n") {
			r.printErr("    " + line)
		}
	}
}

// handleApproval 在无 TTY 场景对高危命令一律拒绝。
//
// 这里刻意不去"模仿交互"（比如从 stdin 读 y/N）：管道里的 stdin 可能就是 prompt 本身，
// 也可能来自另一个脚本，猜测输入语义比直接拒绝危险得多。
// 想放行请显式给 --approve / --trust trusted，让"授权"这件事留在命令行上可见。
func (r *Runner) handleApproval(ctx context.Context, evt engine.Event, format Format, res *Result) {
	ar, ok := evt.Data.(schema.ApprovalRequest)
	if !ok {
		r.printErr("→ 收到无法解析的审批请求，已按拒绝处理")
		return
	}
	res.ApprovalsDenied++
	if format == FormatJSON {
		r.writeJSONLine(evt)
	}
	denied := schema.ApprovalResult{Allowed: false, Reason: "无头模式无审批通道，默认拒绝高危命令"}
	if ar.ResultChannel == nil {
		return
	}
	// ResultChannel 容量为 1，但拒绝必须幂等安全：
	// 引擎侧若已因超时先返回，这里再写会永久阻塞整轮收尾，因此一律超时发送。
	select {
	case ar.ResultChannel <- denied:
	case <-ctx.Done():
	case <-time.After(engineApprovalGrace):
	}
}

// engineApprovalGrace 是回写审批决策的最长等待。
// 正常情况下通道缓冲为 1 会立刻写入；超时分支只是防止引擎已提前放弃时出现 goroutine 悬挂。
const engineApprovalGrace = 2 * time.Second

func (r *Runner) handleGeneric(evt engine.Event, format Format, label string) {
	if format == FormatJSON {
		r.writeJSONLine(evt)
		return
	}
	switch data := evt.Data.(type) {
	case nil:
		return
	case string:
		if data != "" {
			r.printErr(label + ": " + data)
		}
	default:
		if b, err := json.Marshal(data); err == nil {
			r.printErr(label + ": " + string(b))
		}
	}
}

func (r *Runner) maxToolOutput() int {
	if r.MaxToolOutput > 0 {
		return r.MaxToolOutput
	}
	return defaultMaxToolOutput
}

func (r *Runner) printOut(s string) {
	if r.Out == nil {
		return
	}
	fmt.Fprint(r.Out, s)
}

func (r *Runner) printErr(s string) {
	if r.Err == nil {
		return
	}
	fmt.Fprintln(r.Err, s)
}

func (r *Runner) writeJSONLine(evt engine.Event) { r.writeJSON(evt) }

func (r *Runner) writeJSON(v any) {
	if r.Out == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		// 序列化失败（例如载荷里有不可序列化的类型）时降级为可读一行，
		// 宁可让脚本解析失败，也不能把这次事件静默吞掉。
		fmt.Fprintf(r.Out, "{\"type\":\"marshal_error\",\"error\":%q}\n", err.Error())
		return
	}
	fmt.Fprintf(r.Out, "%s\n", b)
}

// compactArgs 把工具入参压成一行，去掉 JSON 的换行与多余空格，避免刷屏。
func compactArgs(args json.RawMessage) string {
	s := strings.TrimSpace(string(args))
	if s == "" {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(s)); err != nil {
		return truncate(s, 200)
	}
	return truncate(compact.String(), 200)
}

func truncate(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	// 按字节截断会切断 UTF-8 字符（终端里表现为问号块），因此以 rune 为单位。
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("...[已截断，共 %d 字符]", len(runes))
}

// summaryLine 是文本模式下的运行摘要，回答"这次跑得怎么样"：
// 调用了多少工具、有几个失败、用了多少 token、是否发生过压缩。
func summaryLine(res Result) string {
	parts := []string{
		fmt.Sprintf("轮次=%d", res.Turns),
		fmt.Sprintf("工具调用=%d", res.ToolCalls),
		fmt.Sprintf("工具失败=%d", res.ToolErrors),
		fmt.Sprintf("token=%d(in)/%d(out)", res.Usage.InputTokens, res.Usage.OutputTokens),
	}
	if res.Compactions > 0 {
		parts = append(parts, fmt.Sprintf("压缩=%d", res.Compactions))
	}
	if res.ApprovalsDenied > 0 {
		parts = append(parts, fmt.Sprintf("高危命令被拒=%d", res.ApprovalsDenied))
	}
	if res.Note != "" {
		parts = append(parts, "收尾说明="+res.Note)
	}
	return "—— " + strings.Join(parts, "  ")
}

// IsTerminal 判断一个标准流是否连接着终端。
// 用 FileInfo 的 ModeCharDevice 判断而非引入 term 依赖：管道/文件重定向时该位为 0。
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ShouldRunHeadless 决定是否走无头路径。
//
// 三条触发条件任一成立即无头：
//  1. 显式要求（--print）：即使有终端也要无头，便于在本地管道结算；
//  2. stdout 不是终端：输出已被重定向/管道接走，TUI 画出来的东西没人看；
//  3. stdin 不是终端：`echo ... | app` 这种用法没有可交互的输入端。
func ShouldRunHeadless(stdin, stdout *os.File, print bool) bool {
	if print {
		return true
	}
	return !IsTerminal(stdout) || !IsTerminal(stdin)
}
