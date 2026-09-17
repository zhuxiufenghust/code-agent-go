package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/engine"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"go.uber.org/zap"
)

func init() {
	cfg := zap.NewDevelopmentConfig()
	cfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	log.NewLogger(&cfg)
}

// TestActionAccumulatesImmediately 验证：每个增量 delta 到达后立即拼接进同一正文块并渲染，
// 收到多少显示多少（网关逐 token 流式则逐字出现；整块到达则整块拼接，无延迟/动画）。
func TestActionAccumulatesImmediately(t *testing.T) {
	m := New("/tmp", "test-model", nil)
	m.width = 100

	ch := make(chan engine.Event)
	m.eventCh = ch

	go func() {
		defer close(ch)
		for _, s := range []string{"你", "好", "，", "这", "是", "流式"} {
			ch <- engine.Event{Type: engine.EventActionDelta, Data: s}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	var prev string
	for i := 0; i < 6; i++ {
		msg := <-ch
		updated, _ := m.Update(eventMsg(msg))
		m = updated.(tuiModel)
		got := strings.Join(m.lines, "")
		if len(got) <= len(prev) {
			t.Fatalf("第 %d 个 delta 后正文应增长, prev=%q now=%q", i, prev, got)
		}
		prev = got
		t.Logf("after delta %d: %q", i, got)
	}
	if prev != "你好，这是流式" {
		t.Fatalf("期望累积正文为 '你好，这是流式', 实际 %q", prev)
	}
}
