// Package subagent 提供"Sub-Agent 委派"能力：主 Agent 把彼此独立的子任务
// 委派给若干子 Agent 并行执行，只把结论回灌主上下文。
//
// 它同时解决三类问题：
//  1. 上下文污染：子 Agent 排查时读过的文件、试过的命令只存在于自己的上下文里；
//  2. 串行瓶颈：互不依赖的子任务可以并行跑；
//  3. 风险失控：每个子任务都有轮次、并发、耗时、回灌长度上限，
//     且子Agent 默认不能再委派（MaxDepth=1）。
//
// 关键取舍：子 Agent 复用父 Agent 的 Provider（凭证与连接配置一致）与工具注册表
// （通过只读视图按需裁剪），因此引入委派不需要任何额外配置。
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/headless"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
)

// ToolName 是委派工具对模型暴露的名字。
const ToolName = "delegate_tool"

// 默认预算：宁可偏小让用户显式放大，也不要让一次委派悄悄烧掉大量配额。
const (
	DefaultMaxDepth       = 1
	DefaultMaxConcurrent  = 3
	DefaultMaxTurns       = 8
	DefaultMaxOutputChars = 8000
	DefaultTimeout        = 3 * time.Minute
)

// Config 是委派工具的运行时预算。
type Config struct {
	// MaxDepth 允许的委派层数；1 表示"主 Agent 可委派，子 Agent 不能再委派"；<=0 表示禁用委派。
	MaxDepth int
	// MaxConcurrent 同一时刻最多运行的子 Agent 数。
	MaxConcurrent int
	// MaxTurns 单个子 Agent 的最大轮次（超出后引擎会主动收尾并给出说明）。
	MaxTurns int
	// MaxOutputChars 单个子任务回灌主上下文的结论字符上限。
	MaxOutputChars int
	// Timeout 单个子任务的墙钟上限。超时不是错误，而是"这条路径先放弃"并回传已有结论。
	Timeout time.Duration
	// WorkDir /	HomeDir 传给子引擎（工具路径解析与会话位置）。
	WorkDir string
	HomeDir string
	// ExcludeTools 不希望子 Agent 看到的工具（如写文件类）。
	ExcludeTools []string
}

func (c Config) withDefaults() Config {
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.MaxTurns <= 0 {
		c.MaxTurns = DefaultMaxTurns
	}
	if c.MaxOutputChars <= 0 {
		c.MaxOutputChars = DefaultMaxOutputChars
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

// Tool 是暴露给主 Agent 的委派工具。
type Tool struct {
	cfg      Config
	registry tools.Registry
	provider provider.LLMProvider
}

// New 创建委派工具。只需三件事：预算、工具注册表、以及能建子引擎的 Provider。
func New(cfg Config, registry tools.Registry, pr provider.LLMProvider) *Tool {
	return &Tool{cfg: cfg.withDefaults(), registry: registry, provider: pr}
}

// GetDefinition 返回工具定义。描述里把"只回传结论 + 是否可嵌套"写清楚：
// 拆不拆、怎么拆是模型的决定，约束只有它知道才会生效。
func (t *Tool) GetDefinition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: ToolName,
		Description: "把多个彼此独立的子任务并行委派给子 Agent 执行。" +
			"子 Agent 与主会话完全隔离（看不到本次会话历史），只回传结论。" +
			"适合信息收集型任务：找调用点、读规范、核对依赖版本等；" +
			"子任务之间不要有先后依赖。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"tasks": map[string]interface{}{
					"type":        "array",
					"description": "彼此独立的子任务列表。",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"id":        map[string]interface{}{"type": "string", "description": "可选任务标识，便于对应结果。"},
							"task":      map[string]interface{}{"type": "string", "description": "给子 Agent 的完整任务描述；它看不到主会话，必须自解释。"},
							"context":   map[string]interface{}{"type": "string", "description": "可选补充上下文，会与任务一起交给子 Agent。"},
							"max_turns": map[string]interface{}{"type": "integer", "description": "可选，覆盖该子任务的轮次上限。"},
						},
						"required": []string{"task"},
					},
				},
				"task": map[string]interface{}{"type": "string", "description": "单任务简写（与 tasks 二选一）。"},
			},
		},
	}
}

type subTask struct {
	ID       string `json:"id"`
	Task     string `json:"task"`
	Context  string `json:"context"`
	MaxTurns int    `json:"max_turns"`
}

// TaskResult 是单个子任务回传给主 Agent 的结构化结论。
// 刻意不包含子 Agent 的完整过程：那正是要隔离掉的东西。
type TaskResult struct {
	ID    string `json:"id"`
	Task  string `json:"task"`
	State string `json:"state"` // ok / timeout / max_turns / error
	// Summary 是子 Agent 的结论（已按 MaxOutputChars 截断）。
	Summary string `json:"summary,omitempty"`
	// JSON 仅当子任务返回合法 JSON 对象时填充，便于下游直接取字段。
	JSON       map[string]any `json:"json,omitempty"`
	Note       string         `json:"note,omitempty"`
	Error      string         `json:"error,omitempty"`
	Turns      int            `json:"turns"`
	ToolCalls  int            `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	Denied     int            `json:"denied_approvals,omitempty"`
	ElapsedMS  int64          `json:"elapsed_ms"`
}

// Execute 并行跑完所有子任务，并把结构化结论返回给主 Agent。
func (t *Tool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.cfg.MaxDepth <= 0 {
		return "", errors.New("委派已被禁用（MaxDepth<=0），请自行完成该任务")
	}
	var in struct {
		Tasks []subTask `json:"tasks"`
		Task  string    `json:"task"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("解析委派参数失败: %w", err)
		}
	}
	tasks := in.Tasks
	if s := strings.TrimSpace(in.Task); s != "" {
		tasks = append([]subTask{{Task: s}}, tasks...)
	}
	if len(tasks) == 0 {
		return "", errors.New("缺少子任务：请提供 task 或 tasks")
	}
	if len(tasks) > t.cfg.MaxConcurrent {
		return "", fmt.Errorf("子任务数 %d 超过并发上限 %d，请先合并或分批", len(tasks), t.cfg.MaxConcurrent)
	}
	for i := range tasks {
		if strings.TrimSpace(tasks[i].Task) == "" {
			return "", fmt.Errorf("第 %d 个子任务的 task 为空", i+1)
		}
		if tasks[i].ID == "" {
			tasks[i].ID = fmt.Sprintf("subtask-%d", i+1)
		}
	}

	results := make([]TaskResult, len(tasks))
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		sem = make(chan struct{}, t.cfg.MaxConcurrent)
	)
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, task subTask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := t.runTask(ctx, task)
			mu.Lock()
			results[idx] = res
			mu.Unlock()
		}(i, task)
	}
	wg.Wait()

	out, err := json.Marshal(map[string]any{"delegated": len(results), "results": results})
	if err != nil {
		return "", fmt.Errorf("序列化委派结果失败: %w", err)
	}
	return string(out), nil
}

// runTask 跑一个子 Agent 并把它的结论收敛成一条 TaskResult。
func (t *Tool) runTask(ctx context.Context, task subTask) TaskResult {
	start := time.Now()
	res := TaskResult{ID: task.ID, Task: truncate(task.Task, 200)}

	maxTurns := task.MaxTurns
	if maxTurns <= 0 || maxTurns > t.cfg.MaxTurns {
		maxTurns = t.cfg.MaxTurns
	}
	childCtx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	// 子 Agent 的工具视图：隐藏不应暴露的工具（含"再委派一层"）。
	childRegistry := newExcludingRegistry(t.registry, append(t.cfg.ExcludeTools, ToolName))
	child := engine.NewAgentEngine(t.provider, childRegistry,
		engine.WithMaxLoopTurns(maxTurns),
		engine.WithWorkDir(t.cfg.WorkDir),
		engine.WithHomeDir(t.cfg.HomeDir),
		engine.WithSessionID(uuid.NewString()),
		// 子会话不落库：它的价值只在结论，中间过程不该污染会话库。
		engine.WithDisablePersistence(),
	)

	var out strings.Builder
	runner := &headless.Runner{Out: &out, Err: &strings.Builder{},
		Format: headless.FormatText, MaxToolOutput: 500}
	result, runErr := runner.Run(childCtx, child, buildChildPrompt(task))

	res.Summary = truncate(result.Text, t.cfg.MaxOutputChars)
	res.Turns = result.Turns
	res.ToolCalls = result.ToolCalls
	res.ToolErrors = result.ToolErrors
	res.Denied = result.ApprovalsDenied
	res.Note = truncate(result.Note, 300)
	if runErr != nil {
		res.Error = truncate(runErr.Error(), 300)
	}
	res.State = stateOf(result, runErr, t.cfg.MaxOutputChars)
	if parsed := parseJSON(result.Text); parsed != nil {
		res.JSON = parsed
	}
	res.ElapsedMS = time.Since(start).Milliseconds()
	return res
}

// stateOf 把一次子任务的收尾原因翻译成主 Agent 可判断的状态。
func stateOf(result headless.Result, runErr error, maxChars int) string {
	switch {
	case runErr != nil && errors.Is(runErr, context.DeadlineExceeded):
		return "timeout"
	case runErr != nil && result.Text == "":
		return "error"
	case result.Note != "":
		return "max_turns"
	case len([]rune(result.Text)) > maxChars:
		return "truncated"
	default:
		return "ok"
	}
}

func parseJSON(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil
	}
	return m
}

// buildChildPrompt 把子任务拼成自解释的输入：子 Agent 看不到主会话任何历史，
// "要做什么、边界是什么"必须由这段话说清楚，否则它只能猜。
func buildChildPrompt(task subTask) string {
	var b strings.Builder
	b.WriteString("请完成下面的单个子任务。你没有本次会话的历史上下文，不要向用户提问，无法完成时也要明确说明失败原因。\n")
	b.WriteString("只给结论，不要复述过程；能用要点就用要点。\n")
	if s := strings.TrimSpace(task.Context); s != "" {
		b.WriteString("\n补充上下文：\n")
		b.WriteString(s)
		b.WriteString("\n")
	}
	b.WriteString("\n子任务：\n")
	b.WriteString(task.Task)
	return b.String()
}

func truncate(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + fmt.Sprintf("...[已截断，共 %d 字符]", len(runes))
}

// excludingRegistry 是主注册表的一个只读视图：把不该给子 Agent 的工具从
// "可见列表"和"可执行路径"两处同时挡掉。只挡一处是不安全的——
// 模型看不到但仍能构造调用（或反过来）都会导致策略被绕过。
type excludingRegistry struct {
	inner    tools.Registry
	excluded sort.StringSlice
}

func newExcludingRegistry(inner tools.Registry, excluded []string) tools.Registry {
	set := make(sort.StringSlice, 0, len(excluded))
	for _, name := range excluded {
		if name != "" {
			set = append(set, name)
		}
	}
	set.Sort()
	return &excludingRegistry{inner: inner, excluded: set}
}

func (r *excludingRegistry) GetAvailableTools() []schema.ToolDefinition {
	defs := r.inner.GetAvailableTools()
	out := defs[:0:0]
	for _, d := range defs {
		if r.isExcluded(d.Name) {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (r *excludingRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	if r.isExcluded(call.Name) {
		return schema.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			IsError:    true,
			Output:     fmt.Sprintf("工具 %s 对子 Agent 不可用（委派策略已排除）", call.Name),
		}
	}
	return r.inner.Execute(ctx, call)
}

func (r *excludingRegistry) Register(tools.Tool) error {
	return errors.New("子 Agent 工具视图只读，新增工具请在主注册表上注册")
}

func (r *excludingRegistry) Use(tools.MiddlewareFunc) {
	// 子 Agent 复用主注册表的既有中间件链（含高危命令审批）：
	// 审批事件由无头 Runner 统一按"无通道即拒绝"处理，不会打断主 Agent。
}

func (r *excludingRegistry) isExcluded(name string) bool {
	idx := sort.SearchStrings([]string(r.excluded), name)
	return idx < len(r.excluded) && r.excluded[idx] == name
}
