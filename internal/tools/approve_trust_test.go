package tools

import (
	"context"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/report"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"github.com/zhuxiufenghust/code-agent-go/internal/trust"
)

// dangerCall 是一条必然命中内置高危规则的调用（见 hooks.MatchDangerous）。
func dangerCall() schema.ToolCall {
	return schema.ToolCall{ID: "call-danger", Name: "bash_tool", Arguments: []byte(`{"command":"rm -rf /tmp/x"}`)}
}

// withChannel 构造一个"有人工审批通道"的管理器，并记录通道是否被用到。
func withChannel(t *testing.T, opts ...func(*ApprovalManager)) (*ApprovalManager, chan schema.ApprovalRequest) {
	t.Helper()
	getter := &stubEmitterGetter{}
	reqCh := make(chan schema.ApprovalRequest, 1)
	getter.em = report.Emitter{
		ApprovalRequired: func(turn int, ar schema.ApprovalRequest) bool {
			reqCh <- ar
			return true
		},
	}
	h := NewApprovalManager([]string{"ls"}, append([]func(*ApprovalManager){WithApprovalTimeout(200 * time.Millisecond)}, opts...)...)
	h.SetEmitGetter(getter)
	return h, reqCh
}

// TestTrustTrustedSkipsPrompt 验证信任态为 trusted 时不再打扰用户。
func TestTrustTrustedSkipsPrompt(t *testing.T) {
	h, reqCh := withChannel(t, WithTrustLevel(trust.Trusted))
	res, err := h.WaitForApproval(context.Background(), dangerCall())
	if err != nil {
		t.Fatalf("不该返回错误: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("trusted 状态应直接放行, 实际拒绝: %s", res.Reason)
	}
	select {
	case <-reqCh:
		t.Fatal("trusted 时不应再发起审批请求")
	default:
	}
}

// TestTrustUndecidedPrompts 验证默认（未表态）行为与改造前一致：有通道就问。
func TestTrustUndecidedPrompts(t *testing.T) {
	h, reqCh := withChannel(t)
	go h.WaitForApproval(context.Background(), dangerCall())
	select {
	case ar := <-reqCh:
		if ar.ToolCall.ID != "call-danger" {
			t.Fatalf("审批请求的工具调用 ID 不符: %s", ar.ToolCall.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("未表态时应当弹审批")
	}
}

// TestTrustUntrustedStillPrompts 验证 untrusted 不是"永远拒绝"而是"每次都问"：
// 彻底拒绝会让用户失去纠偏机会，最终逼着他改用 --approve 一放了之。
func TestTrustUntrustedStillPrompts(t *testing.T) {
	h, reqCh := withChannel(t, WithTrustLevel(trust.Untrusted))
	go h.WaitForApproval(context.Background(), dangerCall())
	select {
	case <-reqCh:
	case <-time.After(time.Second):
		t.Fatal("untrusted 仍应弹审批")
	}
}

// TestTrustDeniesWithoutChannel 是无头/CI 场景的安全底线：
// 问不到人时必须拒绝，即使是用户自己手动设成了 untrusted。
func TestTrustDeniesWithoutChannel(t *testing.T) {
	for _, level := range []trust.Level{trust.Undecided, trust.Untrusted} {
		h := NewApprovalManager(nil, WithTrustLevel(level))
		res, err := h.WaitForApproval(context.Background(), dangerCall())
		if err != nil {
			t.Fatalf("%s: 不该返回错误: %v", level, err)
		}
		if res.Allowed {
			t.Fatalf("%s: 无审批通道时应拒绝高危命令", level)
		}
	}
}

func TestSetTrustPersistsWhenAsked(t *testing.T) {
	dir := t.TempDir()
	h := NewApprovalManager(nil, WithTrustStore(trust.NewStore(dir)))

	if err := h.SetTrust(trust.State{Level: trust.Trusted}, false); err != nil {
		t.Fatalf("会话级授权不该要求落盘成功: %v", err)
	}
	if h.TrustLevel() != trust.Trusted {
		t.Fatalf("SetTrust 未生效: %v", h.TrustLevel())
	}
	if got := trust.NewStore(dir).Load().Level; got == trust.Trusted {
		t.Fatalf("persist=false 时不应写盘, 但磁盘上已是 %v", got)
	}

	// 持久化失败必须让调用方知道："以为以后都不用问了"却又要问，比报错更让人困惑。
	hNoStore := NewApprovalManager(nil)
	if err := hNoStore.SetTrust(trust.State{Level: trust.Trusted}, true); err == nil {
		t.Fatal("未绑定存储时应报错")
	}
}
