package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// fakeStreamer 用一段预排的事件替代真实引擎。
// 事件全部写进带缓冲的 channel 后立即关闭：即使消费者的处理比生产慢（例如要回写审批结果），
// 也不会因为阻塞而掩盖真实的时序缺陷。
type fakeStreamer struct {
	events   []engine.Event
	startErr error
	// block 为真时不投递任何事件、也不关闭 channel，直到 ctx 结束，
	// 用于验证"取消/超时"路径。
	block bool

	gotPrompt string
}

func (f *fakeStreamer) StreamRun(ctx context.Context, prompt string) (<-chan engine.Event, error) {
	f.gotPrompt = prompt
	if f.startErr != nil {
		return nil, f.startErr
	}
	ch := make(chan engine.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	if f.block {
		go func() {
			<-ctx.Done()
			close(ch)
		}()
		return ch, nil
	}
	close(ch)
	return ch, nil
}

func toolStartEvt(id, name, args string) engine.Event {
	return engine.Event{Type: engine.EventToolStart, Turn: 1, Data: schema.ToolCall{
		ID: id, Name: name, Arguments: json.RawMessage(args),
	}}
}

func toolResultEvt(name string, isError bool) engine.Event {
	return engine.Event{Type: engine.EventToolResult, Turn: 1, Data: engine.ToolResultData{
		Result:   schema.ToolResult{ToolCallID: "c1", Name: name, Output: "hello\nworld", IsError: isError},
		Duration: 12 * time.Millisecond,
	}}
}

// scriptedRun 是一次典型的多轮运行：思考 → 工具 → 正文。
func scriptedRun() []engine.Event {
	return []engine.Event{
		{Type: engine.EventThinkingDelta, Turn: 1, Data: "先看看目录"},
		toolStartEvt("c1", "bash_tool", `{"command":"ls"}`),
		toolResultEvt("bash_tool", false),
		{Type: engine.EventActionDelta, Turn: 1, Data: "答案是"},
		{Type: engine.EventActionDelta, Turn: 1, Data: " 42"},
		{Type: engine.EventUsage, Data: &schema.Usage{InputTokens: 100, OutputTokens: 20}},
		{Type: engine.EventDone},
	}
}

func TestRun_TextModeStreamsAnswerAndKeepsDiagnosticsSeparate(t *testing.T) {
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText}
	res, err := r.Run(context.Background(), &fakeStreamer{events: scriptedRun()}, "ls 一下")
	if err != nil {
		t.Fatalf("运行应当成功: %v", err)
	}
	if res.Text != "答案是 42" {
		t.Fatalf("正文拼接不符预期: %q", res.Text)
	}
	if out.String() != "答案是 42" {
		t.Fatalf("stdout 应当只有助手正文, 实际: %q", out.String())
	}
	if res.ToolCalls != 1 || res.ToolErrors != 0 || res.Turns != 1 {
		t.Fatalf("统计不符预期: %+v", res)
	}
	if res.Usage.InputTokens != 100 || res.Usage.OutputTokens != 20 {
		t.Fatalf("用量未被记录: %+v", res.Usage)
	}
	diag := errW.String()
	for _, want := range []string{"bash_tool", "hello", "工具调用=1", "token=100(in)/20(out)"} {
		if !strings.Contains(diag, want) {
			t.Fatalf("诊断输出缺少 %q: %q", want, diag)
		}
	}
	// 默认不打印推理过程：它既长又不稳定，写进诊断会淹没真正有用的工具日志。
	if strings.Contains(diag, "先看看目录") {
		t.Fatalf("默认不应输出推理增量: %q", diag)
	}
}

func TestRun_ShowThinkingIsOptIn(t *testing.T) {
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText, ShowThinking: true}
	if _, err := r.Run(context.Background(), &fakeStreamer{events: scriptedRun()}, "x"); err != nil {
		t.Fatalf("运行应当成功: %v", err)
	}
	if !strings.Contains(errW.String(), "先看看目录") {
		t.Fatalf("开启后应当输出推理增量: %q", errW.String())
	}
}

func TestRun_JSONModeEmitsStructuredLines(t *testing.T) {
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatJSON}
	res, err := r.Run(context.Background(), &fakeStreamer{events: scriptedRun()}, "x")
	if err != nil {
		t.Fatalf("运行应当成功: %v", err)
	}
	if res.Text != "答案是 42" {
		t.Fatalf("JSON 模式下也应保留聚合结果: %q", res.Text)
	}
	// JSON 模式必须保持 stdout 纯净可解析：诊断一个字都不能进去。
	if errW.String() != "" {
		t.Fatalf("JSON 模式不应写 stderr: %q", errW.String())
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("输出不是合法的行序列: %v", err)
	}
	if len(lines) != len(scriptedRun())+1 {
		t.Fatalf("事件行数不符预期: got %d, want %d\n%s", len(lines), len(scriptedRun())+1, out.String())
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("收尾行不是合法 JSON: %v", err)
	}
	if last["type"] != resultEventType {
		t.Fatalf("收尾行 type 应为 result: %v", last)
	}
	data, _ := last["data"].(map[string]any)
	if data["text"] != "答案是 42" {
		t.Fatalf("收尾行正文不符: %v", data["text"])
	}
	if _, ok := lines[0], data["turns"]; ok == "" {
		t.Fatal("收尾行缺少统计字段")
	}
}

func TestRun_ApprovalIsDeniedWithoutHuman(t *testing.T) {
	// 无头模式下高危命令必须被拒绝，而不是尝试从 stdin 猜答案。
	ch := make(chan schema.ApprovalResult, 1)
	events := []engine.Event{
		{Type: engine.EventApprovalRequired, Turn: 1, Data: schema.ApprovalRequest{
			TaskID: "t1", ToolCall: schema.ToolCall{ID: "c1", Name: "bash_tool"},
			Reason: "命中高危规则", ResultChannel: ch,
		}},
		toolResultEvt("bash_tool", true),
		{Type: engine.EventActionDelta, Data: "命令被拦下了"},
		{Type: engine.EventDone},
	}
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText}
	res, err := r.Run(context.Background(), &fakeStreamer{events: events}, "删库")
	if err != nil {
		t.Fatalf("审批被拒不应导致运行失败: %v", err)
	}
	select {
	case got := <-ch:
		if got.Allowed {
			t.Fatalf("无 ACK 通道时不应放行: %+v", got)
		}
		if got.Reason == "" {
			t.Fatal("拒绝应当带理由，便于模型自愈")
		}
	default:
		t.Fatal("审批结果未回写，引擎侧会一直挂到超时")
	}
	if res.ApprovalsDenied != 1 {
		t.Fatalf("拒绝计数不符: %+v", res)
	}
	if res.ToolErrors != 1 {
		t.Fatalf("工具应返回错误由模型自愈: %+v", res)
	}
}

func TestRun_ErrorEventFailsRunButKeepsPartialResult(t *testing.T) {
	events := []engine.Event{
		{Type: engine.EventActionDelta, Data: "半成品"},
		{Type: engine.EventError, Data: "LLM 调用失败: 429"},
	}
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText}
	res, err := r.Run(context.Background(), &fakeStreamer{events: events}, "x")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("错误事件应当让运行失败: %v", err)
	}
	// 已经跑出来的东西不能因为失败就丢：脚本要据此输出已有用量。
	if res.Text != "半成品" {
		t.Fatalf("部分结果丢失: %q", res.Text)
	}
	if out.String() != "半成品" {
		t.Fatalf("已流出的正文应当保留: %q", out.String())
	}
}

func TestRun_ClosedChannelWithoutDoneIsNotAnError(t *testing.T) {
	var out strings.Builder
	r := &Runner{Out: &out, Err: &strings.Builder{}, Format: FormatText}
	res, err := r.Run(context.Background(), &fakeStreamer{
		events: []engine.Event{{Type: engine.EventActionDelta, Data: "早退了"}},
	}, "x")
	if err != nil {
		t.Fatalf("channel 提前关闭不应视为错误: %v", err)
	}
	if res.Text != "早退了" {
		t.Fatalf("正文不符: %q", res.Text)
	}
}

func TestRun_ContextCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	r := &Runner{Out: &strings.Builder{}, Err: &strings.Builder{}, Format: FormatText}
	res, err := r.Run(ctx, &fakeStreamer{block: true}, "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应当返回 context.Canceled: %v", err)
	}
	if res.Text != "" {
		t.Fatalf("取消后不应凭空生成正文: %q", res.Text)
	}
}

func TestRun_StartFailure(t *testing.T) {
	r := &Runner{Out: &strings.Builder{}, Err: &strings.Builder{}, Format: FormatText}
	_, err := r.Run(context.Background(), &fakeStreamer{startErr: errors.New("boom")}, "x")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("启动失败应当被包装后返回: %v", err)
	}
}

func TestRun_MaxTurnNoteIsSurfaced(t *testing.T) {
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText}
	res, err := r.Run(context.Background(), &fakeStreamer{events: []engine.Event{
		{Type: engine.EventFinal, Data: "已达最大步数"},
		{Type: engine.EventDone},
	}}, "x")
	if err != nil {
		t.Fatalf("引擎主动收尾不算失败: %v", err)
	}
	if res.Note != "已达最大步数" {
		t.Fatalf("收尾说明未透出: %+v", res)
	}
	if !strings.Contains(errW.String(), "已达最大步数") {
		t.Fatalf("收尾说明应当在诊断里可见: %q", errW.String())
	}
}

func TestReadPrompt(t *testing.T) {
	t.Run("位置参数优先", func(t *testing.T) {
		got, err := ReadPrompt([]string{"hello", "world"}, strings.NewReader("from stdin"), true)
		if err != nil || got != "hello world" {
			t.Fatalf("位置参数应当被拼接: %q, %v", got, err)
		}
	})
	t.Run("退回 stdin", func(t *testing.T) {
		got, err := ReadPrompt(nil, strings.NewReader("  piped task \n"), true)
		if err != nil || got != "piped task" {
			t.Fatalf("应当读取 stdin: %q, %v", got, err)
		}
	})
	t.Run("非管道时不读 stdin", func(t *testing.T) {
		// stdin 是终端时读它会一直等到 EOF，表现为"卡住"，必须绕开。
		_, err := ReadPrompt(nil, strings.NewReader("ignored"), false)
		if !errors.Is(err, ErrNoPrompt) {
			t.Fatalf("应当返回 ErrNoPrompt: %v", err)
		}
	})
	t.Run("空输入", func(t *testing.T) {
		if _, err := ReadPrompt([]string{"   "}, strings.NewReader(""), true); !errors.Is(err, ErrNoPrompt) {
			t.Fatalf("空白输入应当视为无任务: %v", err)
		}
	})
	t.Run("stdin 过大有上限", func(t *testing.T) {
		got, err := ReadPrompt(nil, strings.NewReader(strings.Repeat("a", maxStdinPrompt+100)), true)
		if err != nil {
			t.Fatalf("超长 stdin 不应报错: %v", err)
		}
		if len(got) != maxStdinPrompt {
			t.Fatalf("应当按上限截断: got %d, want %d", len(got), maxStdinPrompt)
		}
	})
}

func TestParseFormat(t *testing.T) {
	for _, in := range []string{"", "text", "TEXT", " json "} {
		f, err := ParseFormat(in)
		if err != nil {
			t.Fatalf("%q 应当合法: %v", in, err)
		}
		if in == " json " && f != FormatJSON {
			t.Fatalf("json 解析错误: %v", f)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Fatal("未知格式应当报错")
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	got := truncate("你好世界", 2)
	if !strings.HasPrefix(got, "你好") || !strings.Contains(got, "已截断") {
		t.Fatalf("按 rune 截断失败: %q", got)
	}
	if truncate("abc", 10) != "abc" {
		t.Fatal("未超限不应改变文本")
	}
}

func TestCompactArgs(t *testing.T) {
	if got := compactArgs(json.RawMessage(`{"command":  "ls -la"}`)); got != `{"command":"ls -la"}` {
		t.Fatalf("入参应当被压成一行: %q", got)
	}
	if got := compactArgs(json.RawMessage(`not-json`)); got == "" {
		t.Fatal("非法 JSON 应回退原文而不是丢失信息")
	}
	if got := compactArgs(nil); got != "{}" {
		t.Fatalf("空入参应当显示为空对象: %q", got)
	}
}

func TestShouldRunHeadless(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}
	defer r.Close()
	defer w.Close()
	// 管道不是字符设备：只要有一端不是终端就应当无头。
	if !ShouldRunHeadless(r, w, false) {
		t.Fatal("非 TTY 应当走无头路径")
	}
	if ShouldRunHeadless(r, w, false) && !IsTerminal(r) {
		t.Log("（预期）管道被判定为非终端")
	}
	// --print 是无条件的：即便接在终端上也要无头。
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		t.Logf("跳过终端用例（无可用 /dev/tty: %v）", err)
		return
	}
	defer tty.Close()
	if !IsTerminal(tty) {
		t.Fatalf("/dev/tty 应当被判定为终端")
	}
	if ShouldRunHeadless(tty, tty, false) {
		t.Fatal("接在终端上且未显式 --print 时应当走 TUI")
	}
	if !ShouldRunHeadless(tty, tty, true) {
		t.Fatal("--print 应当强制无头")
	}
}

// TestRun_ToolOutputTruncated 保证单个工具结果不会把终端刷爆。
func TestRun_ToolOutputTruncated(t *testing.T) {
	long := strings.Repeat("x", 100)
	var out, errW strings.Builder
	r := &Runner{Out: &out, Err: &errW, Format: FormatText, MaxToolOutput: 10}
	if _, err := r.Run(context.Background(), &fakeStreamer{events: []engine.Event{
		{Type: engine.EventToolResult, Turn: 1, Data: engine.ToolResultData{
			Result: schema.ToolResult{Name: "bash_tool", Output: long},
		}},
		{Type: engine.EventDone},
	}}, "x"); err != nil {
		t.Fatalf("运行应当成功: %v", err)
	}
	if strings.Count(errW.String(), "x") > 20 {
		t.Fatalf("工具输出未被截断: %s", truncate(errW.String(), 200))
	}
	if !strings.Contains(errW.String(), "已截断") {
		t.Fatal("截断应当显式提示")
	}
}

// TestSummaryLineCounters 让"这一次跑得怎么样"可被脚本/人快速核对。
func TestSummaryLineCounters(t *testing.T) {
	got := summaryLine(Result{Turns: 3, ToolCalls: 4, ToolErrors: 1, Compactions: 2,
		ApprovalsDenied: 1, Usage: schema.Usage{InputTokens: 10, OutputTokens: 5}, Note: "卡住了"})
	for _, want := range []string{"轮次=3", "工具调用=4", "工具失败=1", "压缩=2", "高危命令被拒=1", "收尾说明=卡住了"} {
		if !strings.Contains(got, want) {
			t.Fatalf("摘要缺少 %q: %s", want, got)
		}
	}
}

func Example_summaryLine() {
	fmt.Println(summaryLine(Result{Turns: 1, Usage: schema.Usage{InputTokens: 1, OutputTokens: 2}}))
	// Output: —— 轮次=1  工具调用=0  工具失败=0  token=1(in)/2(out)
}
