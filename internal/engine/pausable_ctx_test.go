package engine

import (
	"context"
	"testing"
	"time"
)

// TestPausableTimeoutExpires 验证未被暂停时与 context.WithTimeout 行为一致。
func TestPausableTimeoutExpires(t *testing.T) {
	ctx, p, cancel := newPausableTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_ = p

	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("未暂停时应有截止时间")
	}
	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("期望 DeadlineExceeded, 实际 %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("到期后 Done 应关闭")
	}
}

// TestPausableTimeoutPauseStopsClock 验证暂停期间不计入配额：
// 暂停时长超过总配额后恢复，剩余配额应仍然可用（人工审批不该吃掉运行时间）。
func TestPausableTimeoutPauseStopsClock(t *testing.T) {
	ctx, p, cancel := newPausableTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	p.Pause()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("暂停期间不应有截止时间")
	}
	// 睡过总配额：若计时没暂停，这里就已经超时了。
	time.Sleep(120 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("暂停期间不应超时, 实际 %v", ctx.Err())
	default:
	}

	p.Resume()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("恢复后应重新有截止时间")
	}
	select {
	case <-ctx.Done():
		t.Fatalf("恢复后应还剩约 80ms 配额, 却立即结束: %v", ctx.Err())
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("恢复并耗尽剩余配额后应超时")
	}
}

// TestPausableTimeoutRefCount 验证并发等待按引用计数：
// 多个 Pause 需要等量 Resume 才真正恢复计时。
func TestPausableTimeoutRefCount(t *testing.T) {
	ctx, p, cancel := newPausableTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	p.Pause() // 第 1 个待审批
	p.Pause() // 第 2 个待审批（并发工具调用）
	p.Resume()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("还有 1 个等待未结束时不应恢复计时")
	}
	time.Sleep(80 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("仍有等待时不应超时, 实际 %v", ctx.Err())
	default:
	}
	p.Resume()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("最后一个等待结束后应恢复计时并到期")
	}
}

// TestPausableTimeoutParentCancel 验证父 context 取消能传播。
func TestPausableTimeoutParentCancel(t *testing.T) {
	parent, pcancel := context.WithCancel(context.Background())
	ctx, p, cancel := newPausableTimeout(parent, time.Minute)
	defer cancel()
	p.Pause() // 暂停中也不能挡住父取消
	pcancel()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.Canceled {
			t.Fatalf("期望 Canceled, 实际 %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("父 context 取消后 Done 应关闭")
	}
}
