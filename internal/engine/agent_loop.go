package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/logfmt"
	"github.com/zhuxiufenghust/code-agent-go/internal/memory"
	"github.com/zhuxiufenghust/code-agent-go/internal/prompt"
	"github.com/zhuxiufenghust/code-agent-go/internal/provider"
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

	maxConcurrentTools int
	toolTimeout        time.Duration
	generateRetries    int           // LLM 生成调用最大尝试次数（默认 3）
	generateRetryBase  time.Duration // 重试退避基准（默认 1s）
	workDir            string
	homeDir            string

	// 可选，指定 session ID；若为空则使用默认 session, 记忆是基于sessionID的
	sessionID string

	memory             memory.Session // 可选，nil 表示无持久化
	maxHistoryMsgLimit int            // 历史消息最大保留条数，超过则丢弃最旧的消息
}

func WithToolTimeout(timeout time.Duration) Option {
	return func(e *AgentEngine) {
		e.toolTimeout = timeout
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

type emitter struct {
	// generate 执行一次 LLM 调用，返回响应 Message 和实际 token 用量（可能为 nil）。
	generate  func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)
	toolStart func(turn int, tc schema.ToolCall)
	toolDone  func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration)
	// tokenUpdate 报告当前 context 的 token 用量。
	// 在 LLM 调用前以估算值调用；调用后若有实际用量则以实际值再次调用。
	// tokens = token 数；window = 模型 context window（0 表示未知）。
	tokenUpdate func(tokens, window int)

	// compaction 在上下文发生有效压缩时调用（token 数减少 > 5%）。
	// compaction func(data CompactionData)

	// approval 是人类审批回调，注入到工具执行 context 中。
	// RunStream 模式下通过 EventApprovalRequired 事件驱动 TUI 审批对话框；
	// Run（阻塞）模式下留 nil，HookActionAsk 视为 Allow（向后兼容）。
	// approval hooks.ApprovalFunc
}

func NewAgentEngine(provider provider.LLMProvider, registry tools.Registry, opts ...Option) *AgentEngine {
	engine := &AgentEngine{
		provider: provider,
		registry: registry,
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

func (e *AgentEngine) buildSystemPrompt() string {
	return prompt.BuildSystemPrompt(e.workDir, e.registry.GetAvailableTools())
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

func (e *AgentEngine) generateWithRetry(ctx context.Context, em emitter, turn int, history []schema.Message, toolDefs []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
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
		rspMsg, usage, err = em.generate(ctx, turn, history, toolDefs)
		if err == nil {
			return rspMsg, usage, nil
		}
		if i < maxRetries-1 {
			time.Sleep(baseDelay * (1 << i)) // 指数退避
		}
		lastErr = err
	}
	return nil, nil, lastErr
}

func (e *AgentEngine) executeTools(ctx context.Context, turn int, toolCalls []schema.ToolCall, logPrefix string, em emitter) []schema.ToolResult {
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

			// TODO: 注入审批回调
			// if em.approval != nil {
			//	toolCtx = hooks.WithApprovalFn(toolCtx, em.approval)
			// }

			em.toolStart(turn, tc)

			start := time.Now()
			results[idx] = e.registry.Execute(toolCtx, tc)
			em.toolDone(turn, tc, results[idx], time.Since(start))
		}(i, toolCall)
	}

	wg.Wait()
	return results
}

func (e *AgentEngine) runLoop(ctx context.Context, userPrompt string, logPrefix string, em emitter) error {

	// TODO: 增加session加载历史消息

	var turnCount int
	tools := e.registry.GetAvailableTools()
	llmContext, startIndex := e.LoadHistoryContext(ctx, userPrompt)

	log.Info("run_loop_start", zap.String("user_prompt", userPrompt), zap.Int("history_msg_count", len(llmContext)-1),
		zap.Int("start_index", startIndex), zap.String("history_msgs", logfmt.FormatMsgs(llmContext[:len(llmContext)-1])))
	for {
		turnCount++

		llmStartTime := time.Now()

		rspMsg, usage, err := e.generateWithRetry(ctx, em, turnCount, llmContext, tools)
		llmEndTime := time.Now()
		llmElapsed := llmEndTime.Sub(llmStartTime)

		if err != nil {
			log.Error("llm_call_failed", zap.Int("turn", turnCount), zap.Error(err))
			return err
		}
		llmContext = append(llmContext, *rspMsg)
		log.Info("llm_call_succ ", zap.Int("turn", turnCount), zap.String("role", string(rspMsg.Role)), zap.String("msg_out", rspMsg.Content),
			zap.Int("input_tokens", usage.InputTokens), zap.Int("output_tokens", usage.OutputTokens),
			zap.Duration("llm_elapsed", llmElapsed), zap.Int("tool_calls", len(rspMsg.ToolCalls)))

		// no more tool calls, loop ends
		if len(rspMsg.ToolCalls) == 0 {
			log.Info("loop end")
			break
		}

		toolStart := time.Now()
		results := e.executeTools(ctx, turnCount, rspMsg.ToolCalls, logPrefix, em)
		toolElapsed := time.Since(toolStart)
		hasErr := false
		for _, res := range results {
			if res.IsError {
				hasErr = true
				log.Error("tool_call_failed", zap.Int("turn", turnCount), zap.String("tool_call_id", res.ToolCallID), zap.String("tool_name", res.Name), zap.String("output", res.Output))
			}

			content := res.Output
			if content == "" {
				content = "[工具执行完成，无输出]"
			}
			llmContext = append(llmContext, schema.Message{
				Role:       schema.UserRole,
				Content:    content,
				ToolCallID: res.ToolCallID,
				// 透传结构化错误信号，供 Provider 设置 tool_result.is_error，强化自愈。
				IsError: res.IsError,
			})
		}
		if hasErr {
			return errToolCallFailed
		}
		log.Info("tool_call_succ", zap.Int("turn", turnCount), zap.Duration("tool_elapsed", toolElapsed))
	}
	e.SaveHistoryContext(ctx, llmContext, startIndex)

	return nil
}

func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	em := emitter{
		generate: func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error) {
			return e.provider.Generate(ctx, history, tools)
		},
		toolStart: func(turn int, tc schema.ToolCall) {
			log.Info("tool call done", zap.String("tool_call_id", tc.ID), zap.String("tool_name", tc.Name),
				zap.String("arguments", logfmt.FormatJSON(tc.Arguments)))
		},
		toolDone: func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration) {
			log.Info("tool call done", zap.String("tool_call_id", tc.ID), zap.String("tool_name", tc.Name), zap.Duration("duration", d),
				zap.String("output", result.Output), zap.Bool("is_error", result.IsError))
		},
		tokenUpdate: func(tokens, window int) {
			panic("not imp")
		},
	}

	return e.runLoop(ctx, userPrompt, "agent-run", em)
}
