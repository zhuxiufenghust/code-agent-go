package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/hooks"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

type EmittGetter interface {
	GetEmitter() report.Emitter
}

// defaultApprovalTimeout 是单次人工审批的最长等待时间。
// 超时按“拒绝”处理：宁可让该工具调用失败，也不能让 goroutine 永久悬挂
// （例如 TUI 已退出、用户离开、事件流无人消费）。
const defaultApprovalTimeout = 5 * time.Minute

// ApprovalManager 统一管理当前正在等待人类审批的任务
type ApprovalManager struct {
	mu         sync.Mutex
	dangerCmds []string
	emGetter   EmittGetter
	timeout    time.Duration
}

func (h *ApprovalManager) SetEmitGetter(emGetter EmittGetter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.emGetter = emGetter
}

// WithApprovalTimeout 设置单次人工审批的等待上限，覆盖 defaultApprovalTimeout。
func WithApprovalTimeout(d time.Duration) func(*ApprovalManager) {
	return func(h *ApprovalManager) {
		if d > 0 {
			h.timeout = d
		}
	}
}

func NewApprovalManager(dangerCmds []string, opts ...func(*ApprovalManager)) *ApprovalManager {
	h := &ApprovalManager{
		dangerCmds: dangerCmds,
		timeout:    defaultApprovalTimeout,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// emitter 并发安全地取当前 emitter；未注入时返回零值（所有回调为 nil）。
func (h *ApprovalManager) emitter() report.Emitter {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.emGetter == nil {
		return report.Emitter{}
	}
	return h.emGetter.GetEmitter()
}

// dangerPatterns 返回配置黑名单的快照（dangerCmds 理论上可在运行期追加，读取需加锁）。
func (h *ApprovalManager) dangerPatterns() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.dangerCmds))
	copy(out, h.dangerCmds)
	return out
}

// approvalTimeout 返回单次审批的等待上限。
func (h *ApprovalManager) approvalTimeout() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timeout <= 0 {
		return defaultApprovalTimeout
	}
	return h.timeout
}

// IsDangerousCommand 判断该工具调用是否需要人工审批：
// 内置高危规则（hooks.MatchDangerous，开箱即有底线）+ 配置黑名单（dangerCmds）。
// 对 bash 只匹配真正的命令字符串，避免把 JSON 键名/路径等无关片段算进匹配范围。
func (h *ApprovalManager) IsDangerousCommand(toolName string, args string) bool {
	// 对于纯读取的工具，默认 YOLO 模式，全部放行
	if toolName != "bash_tool" && toolName != "edit_tool" && toolName != "write_tool" {
		return false
	}

	// 针对 bash 的高危模式匹配
	if toolName == "bash_tool" {
		if ok, _ := hooks.MatchDangerous(BashCommand(args)); ok {
			return true
		}
		for _, p := range h.dangerPatterns() {
			if matched, _ := regexp.MatchString(p, args); matched {
				return true
			}
		}
	}
	return false
}

// BashCommand 从 bash_tool 的参数 JSON 中取出待执行的命令；
// 解析失败时退回原始参数，保证"宁可多问一次"而不是漏判。
func BashCommand(args string) string {
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &payload); err == nil && payload.Command != "" {
		return payload.Command
	}
	return args
}

// WaitForApproval 注册一个等待人工审批的任务并阻塞,
// 直到 TUI 通过 Resolve 把结果送回对应的 channel。
// taskID 由调用方保证唯一(通常复用 ToolCall.ID)。
func (h *ApprovalManager) WaitForApproval(ctx context.Context, call schema.ToolCall) (schema.ApprovalResult, error) {
	if !h.IsDangerousCommand(call.Name, string(call.Arguments)) {
		return schema.ApprovalResult{Allowed: true, Reason: "安全命令，无需人工审批"}, nil
	}
	em := h.emitter()
	if em.ApprovalRequired == nil {
		// 非交互/无头模式（Run 模式、CI、无 TUI）没有把请求送给人类的通道：
		// 这里按 deny 处理并记录，绝不能默认放行——否则高危命令在无人看管时被执行。
		log.Warn("高危命令被拒绝：非交互模式无审批通道",
			zap.String("tool", call.Name), zap.String("tool_call_id", call.ID))
		return schema.ApprovalResult{Allowed: false, Reason: "非交互模式无审批通道，默认拒绝高危命令"}, nil
	}
	// 每次审批独立计时：即便所在工具调用本身没有超时设置，也必须给人类决策一个上限，
	// 否则 TUI 未响应（崩溃/用户离开）时该 goroutine 会一直挂到整轮运行结束。
	waitCtx, cancel := context.WithTimeout(ctx, h.approvalTimeout())
	defer cancel()

	ch := make(chan schema.ApprovalResult, 1)
	deadline, _ := waitCtx.Deadline()
	delivered := em.ApprovalRequired(0, schema.ApprovalRequest{
		TaskID:        call.ID,
		ToolCall:      call,
		Reason:        "命令命中高危黑名单，需人工审批",
		ResultChannel: ch,
		Deadline:      deadline,
	})
	if !delivered {
		// 事件没送达（事件流已关闭/已取消）：立即拒绝，不再空等到超时。
		return schema.ApprovalResult{Allowed: false, Reason: "审批请求未能送达，已拒绝"}, nil
	}
	// 通知引擎“本次人工等待已结束”（批准/拒绝/超时/取消都算），用于恢复运行计时。
	if em.ApprovalResolved != nil {
		defer em.ApprovalResolved()
	}
	select {
	case result, ok := <-ch:
		if !ok {
			return schema.ApprovalResult{Allowed: false, Reason: "审批被取消"}, nil
		}
		return result, nil
	case <-waitCtx.Done():
		return schema.ApprovalResult{Allowed: false, Reason: "审批超时或被取消"}, waitCtx.Err()
	}
}
