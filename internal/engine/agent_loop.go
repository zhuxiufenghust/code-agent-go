package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/context_mng"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"
	"github.com/zhuxiufenghust/code-agent-go/internal/memory"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/tools"
	"go.uber.org/zap"
)

var errToolCallFailed = errors.New("tool call failed")

type Option func(*AgentEngine)

type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry
	session  memory.Session // 可选，nil 表示无持久化
	recovery *context_mng.RecoveryManager

	maxConcurrentTools int
	toolTimeout        time.Duration
	generateRetries    int           // LLM 生成调用最大尝试次数（默认 3）
	generateRetryBase  time.Duration // 重试退避基准（默认 1s）
	workDir            string
	homeDir            string
	// usage 是本会话（本 AgentEngine）累计的 token 用量，
	// 由 generateWithRetry 在每次 LLM 调用成功后累计，与 Provider 级用量解耦。
	usage schema.Usage

	// 可选，指定 session ID；若为空则使用默认 session, 记忆是基于sessionID的
	sessionID string

	memory             memory.Session // 可选，nil 表示无持久化
	maxHistoryMsgLimit int            // 历史消息最大保留条数，超过则丢弃最旧的消息

	maxLoopTurns int            // 可选，限制单次 runLoop 最大轮次，0 表示无限制
	emitter      report.Emitter // 可选，事件回调接口

}

func WithToolTimeout(timeout time.Duration) Option {
	return func(e *AgentEngine) {
		e.toolTimeout = timeout
	}
}
func WithMaxLoopTurns(max int) Option {
	return func(e *AgentEngine) {
		e.maxLoopTurns = max
	}
}

func WithMaxConcurrentTools(max int) Option {
	return func(e *AgentEngine) {
		e.maxConcurrentTools = max
	}
}

func WithGenerateRetries(retries int) Option {
	return func(e *AgentEngine) {
		e.generateRetries = retries
	}
}
func WithGenerateRetryBase(base time.Duration) Option {
	return func(e *AgentEngine) {
		e.generateRetryBase = base
	}
}
func WithWorkDir(dir string) Option {
	return func(e *AgentEngine) {
		e.workDir = dir
	}
}
func WithHomeDir(dir string) Option {
	return func(e *AgentEngine) {
		e.homeDir = dir
	}
}
func WithMaxHistoryMsgLimit(limit int) Option {
	return func(e *AgentEngine) {
		e.maxHistoryMsgLimit = limit
	}
}
func WithSessionID(sessID string) Option {
	return func(e *AgentEngine) {
		e.sessionID = sessID
	}
}

func (e *AgentEngine) GetEmitter() report.Emitter {
	return e.emitter
}

func NewAgentEngine(provider provider.LLMProvider, registry tools.Registry, opts ...Option) *AgentEngine {
	engine := &AgentEngine{
		provider: provider,
		registry: registry,
		recovery: context_mng.NewRecoveryManager(),
	}
	for _, opt := range opts {
		opt(engine)
	}

	var err error
	engine.session, err = memory.NewSQLiteSession(engine.sessionID, engine.homeDir)
	if err != nil {
		log.Error("failed to create SQLite session", zap.Error(err))
		engine.session, _ = memory.NewMemorySession(engine.sessionID) // fallback to in-memory session
	}
	if engine.maxHistoryMsgLimit <= 0 || engine.maxHistoryMsgLimit > 100 {
		engine.maxHistoryMsgLimit = 100 // default limit
	}

	return engine
}

func (e *AgentEngine) UpdateEmitter(em report.Emitter) {
	e.emitter = em
}

func (e *AgentEngine) buildSystemPrompt() string {
	return context_mng.BuildSystemPrompt(e.workDir, e.registry.GetAvailableTools())
}

// 加载历史上下文消息，返回包含系统提示、历史消息和当前用户输入的完整对话上下文。
func (e *AgentEngine) LoadHistoryContext(ctx context.Context, userInput string) ([]schema.Message, int) {
	// TODO: 实现基于 sessionID 的历史消息加载
	userMsg := schema.Message{
		Role:    schema.UserRole,
		Content: userInput,
	}
	msgs, err := e.session.GetMessages(ctx, e.maxHistoryMsgLimit)
	if err != nil || len(msgs) == 0 {
		log.Warn("failed to load history context", zap.String("session_id", e.sessionID))
		systemPrompt := e.buildSystemPrompt()
		systemPromptMsg := schema.Message{
			Role:    schema.SystemRole,
			Content: systemPrompt,
		}
		return []schema.Message{systemPromptMsg, userMsg}, 0
	}
	startIndex := len(msgs)
	msgs = append(msgs, schema.Message{
		Role:    schema.UserRole,
		Content: userInput,
	})
	return msgs, startIndex
}

// SaveHistoryContext 将当前轮次的消息追加到历史上下文中, 只保存新消息
func (e *AgentEngine) SaveHistoryContext(ctx context.Context, msgs []schema.Message, startIndex int) {
	if e.session == nil || len(msgs) == 0 || startIndex >= len(msgs) {
		return
	}
	err := e.session.AddMessages(ctx, msgs[startIndex:])
	if err != nil {
		log.Error("failed to save history context", zap.Error(err), zap.String("session_id", e.sessionID))
	}
}

// addUsage 累计会话级用量。
// 刻意不复用 provider.GetUsage()：那是 Provider 级累计，
// 一旦多 Provider 路由或复用同一 provider 跑多个会话就会串账；
// 用量应当挂在会话（AgentEngine）上。
func (e *AgentEngine) addUsage(u *schema.Usage) {
	if u == nil {
		return
	}
	e.usage.InputTokens += u.InputTokens
	e.usage.OutputTokens += u.OutputTokens
}

// reportUsage 把当前累计用量推给客户端（TUI 展示 / 无头记录）。
// 未设置回调（如非流式模式）时直接跳过，不产生任何副作用。
func (e *AgentEngine) reportUsage(ctx context.Context, turn int) {
	if e.emitter.TokenUpdate == nil {
		return
	}
	snapshot := e.usage // 传快照，避免调用方持有并修改内部状态
	e.emitter.TokenUpdate(ctx, turn, &snapshot)
}

// Usage 返回本会话累计用量快照（供外部展示/测试断言）。
func (e *AgentEngine) Usage() schema.Usage { return e.usage }

func (e *AgentEngine) generateWithRetry(ctx context.Context, turn int, history []schema.Message, toolDefs []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
	var rspMsg *schema.Message
	var usage *schema.Usage
	var err error
	maxRetries := e.generateRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if maxRetries > 10 {
		maxRetries = 10
	}
	baseDelay := e.generateRetryBase
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		rspMsg, usage, err = e.emitter.Generate(ctx, turn, history, toolDefs)
		if err == nil {
			// 唯一的用量采集点：所有 LLM 调用（流式/非流式、含重试）都经过本函数，
			// 因此用量逻辑只在这里出现一次，runLoop 不必再关心"从哪取、何时报"。
			// 工具执行本身不消耗 token，故不在 executeTools 后重复上报
			// （工具输出会作为下一轮的 input，体现在下一次 LLM 调用的用量里）。
			e.addUsage(usage)
			e.reportUsage(ctx, turn)
			return rspMsg, usage, nil
		}
		if i < maxRetries-1 {
			time.Sleep(baseDelay * (1 << i)) // 指数退避
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

func (e *AgentEngine) executeTools(ctx context.Context, turn int, toolCalls []schema.ToolCall, logPrefix string) []schema.ToolResult {
	results := make([]schema.ToolResult, len(toolCalls))
	var wg sync.WaitGroup

	var sem chan struct{}
	if e.maxConcurrentTools > 0 {
		sem = make(chan struct{}, e.maxConcurrentTools)
	}

	for i, toolCall := range toolCalls {
		wg.Add(1)
		go func(idx int, tc schema.ToolCall) {
			defer wg.Done()

			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}

			toolCtx := ctx
			var cancel context.CancelFunc
			if e.toolTimeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, e.toolTimeout)
				defer cancel()
			}
			e.emitter.ToolStart(turn, tc)

			start := time.Now()
			results[idx] = e.registry.Execute(toolCtx, tc)
			e.emitter.ToolDone(turn, tc, results[idx], time.Since(start))
		}(i, toolCall)
	}

	wg.Wait()
	return results
}

// stuckRepeatThreshold 是"连续重复相同工具调用"的判定阈值：
// repeatCount 计的是"与上一轮相同的次数"，取 2 表示同一调用连续出现 3 轮即判定卡住，
// 既不误伤自愈场景下的一次重试，也不会让死循环跑太久。
const stuckRepeatThreshold = 2

// toolCallsSignature 把一轮的工具调用序列化成可比对的签名（顺序敏感）。
// 参数为空的调用（如纯文本轮）返回空串，不参与卡住判定。
func toolCallsSignature(calls []schema.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Name)
		b.WriteByte('(')
		b.Write(c.Arguments)
		b.WriteString(");")
	}
	return b.String()
}

func (e *AgentEngine) runLoop(ctx context.Context, userPrompt string, logPrefix string) error {

	// TODO: 增加session加载历史消息

	var turnCount int
	tools := e.registry.GetAvailableTools()
	llmContext, startIndex := e.LoadHistoryContext(ctx, userPrompt)

	// finalize 是统一收尾：把终止原因作为"最终文本回复"写入上下文与会话，
	// 并通过 emitter.Final 通知客户端（TUI 能看到"为什么停了"），而不是无声结束。
	finalize := func(reason string) {
		log.Warn("run_loop_stopped", zap.String("reason", reason), zap.Int("turn", turnCount),
			zap.String("session_id", e.sessionID))
		llmContext = append(llmContext, schema.Message{Role: schema.AssistantRole, Content: reason})
		if e.emitter.Final != nil {
			e.emitter.Final(reason)
		}
	}

	// lastCallSig / repeatCount 用于"卡住检测"：连续多轮提交完全相同的
	// (name+args) 说明模型在原地打转（或自愈失败），继续只会烧钱，必须终止。
	var lastCallSig string
	var repeatCount int

	log.Info("run_loop_start", zap.String("user_prompt", userPrompt), zap.Int("history_msg_count", len(llmContext)-1),
		zap.Int("start_index", startIndex), zap.String("history_msgs", logfmt.FormatMsgs(llmContext[:len(llmContext)-1])))
	for {
		turnCount++
		if e.maxLoopTurns > 0 && turnCount > e.maxLoopTurns {
			log.Warn("max loop turns reached", zap.Int("turn", turnCount), zap.String("session_id", e.sessionID),
				zap.String("history_msgs", logfmt.FormatMsgs(llmContext)))
			finalize(fmt.Sprintf("[已停止] 达到最大轮次上限 %d 轮，任务未自然收敛。请缩小目标后重试。", e.maxLoopTurns))
			break
		}

		llmStartTime := time.Now()

		rspMsg, usage, err := e.generateWithRetry(ctx, turnCount, llmContext, tools)
		llmEndTime := time.Now()
		llmElapsed := llmEndTime.Sub(llmStartTime)

		if err != nil {
			log.Error("llm_call_failed", zap.Int("turn", turnCount), zap.Error(err))
			return err
		}
		// 用量采集与上报已收敛到 generateWithRetry（成功分支），这里不再重复处理：
		// 主循环只管业务，观测逻辑不侵入。
		// Emitter 约定：err 为 nil 时 Message 不应为 nil。这里兜住实现违规（或 mock），
		// 否则下面的 *rspMsg 会空指针让整轮运行直接崩溃。
		if rspMsg == nil {
			log.Error("llm_returned_empty_msg", zap.Int("turn", turnCount))
			return errors.New("llm 返回空响应")
		}
		llmContext = append(llmContext, *rspMsg)
		// usage 允许为 nil（Emitter 约定：无实际用量时返回 nil），不能无条件解引用。
		var inTokens, outTokens int
		if usage != nil {
			inTokens, outTokens = usage.InputTokens, usage.OutputTokens
		}
		log.Info("llm_call_succ ", zap.Int("turn", turnCount), zap.String("role", string(rspMsg.Role)), zap.String("msg_out", rspMsg.Content),
			zap.Int("input_tokens", inTokens), zap.Int("output_tokens", outTokens),
			zap.Duration("llm_elapsed", llmElapsed), zap.Int("tool_calls", len(rspMsg.ToolCalls)))

		// no more tool calls, loop ends
		if len(rspMsg.ToolCalls) == 0 {
			log.Info("loop end")
			break
		}

		// 卡住检测：与上一轮的 (name+args) 完全相同则累加，达到阈值即判定卡住。
		if sig := toolCallsSignature(rspMsg.ToolCalls); sig != "" && sig == lastCallSig {
			repeatCount++
		} else {
			repeatCount = 0
		}
		lastCallSig = toolCallsSignature(rspMsg.ToolCalls)
		if repeatCount >= stuckRepeatThreshold {
			log.Warn("stuck_detected", zap.Int("turn", turnCount), zap.Int("repeat", repeatCount),
				zap.String("signature", lastCallSig), zap.String("session_id", e.sessionID))
			finalize(fmt.Sprintf("[已停止] 检测到连续 %d 轮重复提交完全相同的工具调用，判定为卡住。请换一种思路或补充信息后重试。",
				repeatCount+1))
			break
		}

		// 工具执行不消耗 token，因此这里不上报用量：
		// 工具输出会作为下一轮的 input，体现在下一次 LLM 调用的用量里。
		results := e.executeTools(ctx, turnCount, rspMsg.ToolCalls, logPrefix)

		for _, res := range results {
			finalOutput := res.Output
			if res.IsError {
				log.Error("tool_call_failed", zap.Int("turn", turnCount), zap.String("tool_call_id", res.ToolCallID),
					zap.String("tool_name", res.Name), zap.String("output", res.Output))
				finalOutput = e.recovery.AnalyzeAndInject(ctx, res.Name, res.Output)
				// 自愈提示注入观测：当返回内容与原错误不一致时，说明 RecoveryManager 命中并注入了 [系统救援指南]，
				// 便于端到端验证时从日志确认 recover 机制是否生效。
				if finalOutput != res.Output {
					log.Info("recovery_hint_injected", zap.Int("turn", turnCount),
						zap.String("tool_name", res.Name), zap.String("hint", finalOutput))
				}
			} else {
				log.Info("tool_call_succ", zap.Int("turn", turnCount), zap.String("tool_call_id", res.ToolCallID),
					zap.String("tool_name", res.Name), zap.String("output", res.Output))
			}

			content := res.Output
			if content == "" {
				content = "[工具执行完成，无输出]"
			}
			llmContext = append(llmContext, schema.Message{
				Role:       schema.UserRole,
				Content:    finalOutput,
				ToolCallID: res.ToolCallID,
				// 透传结构化错误信号，供 Provider 设置 tool_result.is_error，强化自愈。
				IsError: res.IsError,
			})
		}
	}
	e.SaveHistoryContext(ctx, llmContext, startIndex)

	return nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	em := report.Emitter{
		Generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			return e.provider.Generate(ctx, history, tools)
		},
		ToolStart: func(turn int, tc schema.ToolCall) {
			log.Info("tool call done", zap.String("tool_call_id", tc.ID), zap.String("tool_name", tc.Name),
				zap.String("arguments", logfmt.FormatJSON(tc.Arguments)))
		},
		ToolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			log.Info("tool call done", zap.String("tool_call_id", tc.ID), zap.String("tool_name", tc.Name), zap.Duration("duration", d),
				zap.String("output", result.Output), zap.Bool("is_error", result.IsError))
		},
		// 阻塞模式没有事件流可以把用量推给客户端：只记日志，
		// panic 会让非流式调用一跑就崩（无头/CI 场景必踩）。
		TokenUpdate: func(ctx context.Context, turn int, usage *schema.Usage) {
			if usage == nil {
				return
			}
			log.Debug("token_usage", zap.Int("turn", turn),
				zap.Int("input_tokens", usage.InputTokens), zap.Int("output_tokens", usage.OutputTokens))
		},
		// 阻塞模式没有事件流可以把审批请求送给人：ApprovalRequired 保持 nil，
		// ApproveManager 会据此“默认放行”（向后兼容）。若在这里放一个必然返回 false
		// 的空实现，反而会让高危命令在无人审批的场景下被静默拒绝。
	}
	e.UpdateEmitter(em)
	return e.runLoop(ctx, userPrompt, "agent-run")
}
