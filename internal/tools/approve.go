package tools

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/hooks"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/trust"
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
	// trustLevel 是本次进程生效的信任态，决定危险命令是"直接放行/询问/直接拒绝"。
	// 默认 Undecided，等价于改造前的行为（有通道就问，没通道就拒）。
	trustLevel trust.Level
	// trustStore 为 nil 表示不做持久化（只有 --approve 这种会话级授权）。
	trustStore *trust.Store
}

// WithTrustLevel 设置本次进程生效的信任态（会话级，不写盘）。
func WithTrustLevel(l trust.Level) func(*ApprovalManager) {
	return func(h *ApprovalManager) { h.trustLevel = l }
}

// WithTrustStore 绑定信任态持久化存储，使信任变更能跨进程保持。
func WithTrustStore(s *trust.Store) func(*ApprovalManager) {
	return func(h *ApprovalManager) { h.trustStore = s }
}

// TrustLevel 返回当前生效的信任态。
func (h *ApprovalManager) TrustLevel() trust.Level { return h.trustLevel }

// SetTrust 变更信任态。persist 为真且已绑定 Store 时落盘，
// 落盘失败必须让调用方知道：否则用户以为"以后都不用问了"，重启后又开始问。
func (h *ApprovalManager) SetTrust(st trust.State, persist bool) error {
	h.mu.Lock()
	h.trustLevel = st.Level
	h.mu.Unlock()
	if !persist {
		return nil
	}
	if h.trustStore == nil {
		return errors.New("未绑定信任态存储，无法持久化")
	}
	return h.trustStore.Save(st)
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
// 对 bash 只匹配从参数 JSON 中提取出的命令字符串，避免把 JSON 键名/路径等无关片段算进匹配范围。
func (h *ApprovalManager) IsDangerousCommand(toolName string, args string) bool {
	// 对于纯读取的工具，默认 YOLO 模式，全部放行
	if toolName != "bash_tool" && toolName != "edit_tool" && toolName != "write_tool" {
		return false
	}

	// 针对 bash 的高危模式匹配
	if toolName == "bash_tool" {
		command := BashCommand(args)
		if ok, _ := hooks.MatchDangerous(command); ok {
			return true
		}
		for _, p := range h.dangerPatterns() {
			if matchDangerPattern(p, command) {
				return true
			}
		}
	}
	return false
}

// matchDangerPattern 判断配置黑名单项是否命中命令。
// 黑名单按正则理解（如 "kubectl\s+delete"），但整体加 \b 词边界锚定：
// 不加边界时 "ls" 会命中 "tools"、"lsblk" 等任何含该子串的文本，
// 导致审批弹窗远比配置预期频繁。
func matchDangerPattern(pattern, command string) bool {
	re, err := regexp.Compile(`\b(?:` + pattern + `)\b`)
	if err != nil {
		// 非法正则按不命中处理：黑名单写错不应拦住所有命令。
		return false
	}
	return re.MatchString(command)
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
	// 信任态先于"有没有通道"生效：Trusted 时即便无人值守也放行（这是用户显式授权的语义），
	// Undecided/Untrusted 且问不到人时一律拒绝。
	switch trust.Decide(h.trustLevel, em.ApprovalRequired != nil) {
	case trust.DecisionAllow:
		log.Info("高危命令按信任态放行", zap.String("tool", call.Name),
			zap.String("trust_level", h.trustLevel.String()))
		return schema.ApprovalResult{Allowed: true, Reason: "信任态已授权（trusted），无需再次确认"}, nil
	case trust.DecisionDeny:
		reason := "当前信任态不允许在无交互通道下执行高危命令，已拒绝"
		if em.ApprovalRequired == nil {
			reason = "非交互模式无审批通道，默认拒绝高危命令"
		}
		log.Warn("高危命令被拒绝", zap.String("tool", call.Name),
			zap.String("trust_level", h.trustLevel.String()),
			zap.String("tool_call_id", call.ID), zap.String("reason", reason))
		return schema.ApprovalResult{Allowed: false, Reason: reason}, nil
	}
	// DecisionAsk：走人工审批流程
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
