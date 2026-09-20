package report

import (
	"context"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type Emitter struct {
	// generate 执行一次 LLM 调用，返回响应 Message 和实际 token 用量（可能为 nil）。
	Generate  func(ctx context.Context, turn int, history []schema.Message, tools []schema.ToolDefinition) (*schema.Message, *schema.Usage, error)
	ToolStart func(turn int, tc schema.ToolCall)
	ToolDone  func(turn int, tc schema.ToolCall, result schema.ToolResult, d time.Duration)
	// tokenUpdate 报告当前 context 的 token 用量。
	// 在 LLM 调用前以估算值调用；调用后若有实际用量则以实际值再次调用。
	// tokens = token 数；window = 模型 context window（0 表示未知）。
	TokenUpdate func(ctx context.Context, turn int, usage *schema.Usage)

	// compaction 在上下文发生有效压缩时调用（token 数减少 > 5%）。
	// compaction func(data CompactionData)

	// approval 是人类审批回调，注入到工具执行 context 中。
	// RunStream 模式下通过 EventApprovalRequired 事件驱动 TUI 审批对话框；
	// Run（阻塞）模式下留 nil，HookActionAsk 视为 Allow（向后兼容）。
	// approval hooks.ApprovalFunc

	// approvalRequired 在工具需要人工审批时，向客户端事件流推送 EventApprovalRequired。
	// 返回值表示事件是否真的送达（事件流已关闭/已取消时为 false），
	// 调用方据此立即按“拒绝”处理，避免继续空等到超时。
	ApprovalRequired func(turn int, ar schema.ApprovalRequest) bool

	// approvalResolved 在人工审批出结果后（批准/拒绝/超时/取消都算）由审批方回调，
	// 用于让引擎恢复被暂停的运行计时。为 nil 表示无人关心（如非流式模式）。
	ApprovalResolved func()

	// final 在 runLoop 主动收尾时（达到最大轮次 / 判定卡住）输出最终文本，
	// 使流式客户端也能看到"为什么停了"，而不是无声结束。为 nil 表示无人关心。
	Final func(text string)
}
