package engine

import (
	"context"
	"sync"
	"time"
)

// Pauser 用于把某段等待时间排除在运行计时之外（典型场景：等待人工审批）。
// Pause/Resume 必须成对调用；内部按引用计数，支持多个并发等待：
// 第一个 Pause 真正停表，最后一个 Resume 才重新起表。
type Pauser interface {
	Pause()
	Resume()
}

// pausableTimeoutCtx 是一个可暂停的超时 context。
//
// 它与 context.WithTimeout 语义一致（到期或父 context 取消时 Done 关闭、Err 给出原因），
// 区别是：暂停期间不计时，且 Deadline() 返回 ok=false。
// 用途：给整轮 agent 运行设置总时长上限，但把“等人类点审批”的时间排除掉，
// 否则用户思考/操作的时间会吃掉运行配额，1 分钟就把整轮判成超时。
type pausableTimeoutCtx struct {
	parent context.Context

	mu        sync.Mutex
	remaining time.Duration // 剩余配额：暂停时冻结，恢复时续用
	deadline  time.Time     // 当前截止时间；零值表示处于暂停中
	pending   int           // Pause 的引用计数
	timer     *time.Timer
	done      chan struct{}
	err       error
}

// newPausableTimeout 返回带 d 时长上限的 context、用于暂停/恢复计时的 Pauser，
// 以及提前结束用的 CancelFunc。cancel 必须被调用，以释放内部 timer 与 goroutine。
func newPausableTimeout(parent context.Context, d time.Duration) (context.Context, Pauser, context.CancelFunc) {
	c := &pausableTimeoutCtx{
		parent:    parent,
		remaining: d,
		deadline:  time.Now().Add(d),
		timer:     time.NewTimer(d),
		done:      make(chan struct{}),
	}
	go func() {
		select {
		case <-c.timer.C:
			c.finish(context.DeadlineExceeded)
		case <-c.parent.Done():
			c.finish(c.parent.Err())
		case <-c.done:
		}
	}()
	var once sync.Once
	return c, c, func() { once.Do(func() { c.finish(context.Canceled) }) }
}

// finish 结束 context（首次生效）。不能在持锁状态下调用。
func (c *pausableTimeoutCtx) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	c.stopTimerLocked()
	close(c.done)
}

func (c *pausableTimeoutCtx) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.deadline.IsZero() {
		return time.Time{}, false
	}
	return c.deadline, true
}

func (c *pausableTimeoutCtx) Done() <-chan struct{} { return c.done }

func (c *pausableTimeoutCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *pausableTimeoutCtx) Value(key any) any { return c.parent.Value(key) }

func (c *pausableTimeoutCtx) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending++
	if c.pending > 1 {
		return // 已有等待在暂停中，只累加引用
	}
	// 首次进入等待：冻结剩余配额并停表。
	if left := time.Until(c.deadline); c.err == nil && left > 0 {
		c.remaining = left
	} else {
		c.remaining = 0
	}
	c.deadline = time.Time{}
	c.stopTimerLocked()
}

func (c *pausableTimeoutCtx) Resume() {
	c.mu.Lock()
	if c.pending > 0 {
		c.pending--
	}
	if c.pending > 0 {
		c.mu.Unlock()
		return // 还有其他等待，继续停表
	}
	if c.err != nil {
		c.mu.Unlock()
		return // 已经结束，不要再起表
	}
	if c.remaining <= 0 {
		c.mu.Unlock()
		c.finish(context.DeadlineExceeded)
		return
	}
	// 最后一个等待结束：按剩余配额重新起表。
	c.stopTimerLocked()
	c.deadline = time.Now().Add(c.remaining)
	c.timer.Reset(c.remaining)
	c.mu.Unlock()
}

// stopTimerLocked 停表并排空已触发但未取走的时间信号，保证后续 Reset 有效。
func (c *pausableTimeoutCtx) stopTimerLocked() {
	if !c.timer.Stop() {
		select {
		case <-c.timer.C:
		default:
		}
	}
}
