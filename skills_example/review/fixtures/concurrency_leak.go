// Package fixtures 包含刻意植入缺陷的样本代码，供 review skill 自测使用。
//
// 这些文件不属于 code-agent-go 业务代码，也不参与主模块构建
// （本目录有自己的 go.mod，Go 会自动排除嵌套模块）。
// 请勿"修复"这些缺陷——它们的存在就是回归测试的基线。
package fixtures

import (
	"context"
	"strings"
	"sync"
	"time"
)

func process(item string) string { return strings.TrimSpace(item) }

// State 是被并发访问的状态。
type State struct {
	mu    sync.Mutex
	items []string
}

// Snapshot 复制一份当前状态用于上报。
func (s State) Snapshot() []string {
	return append([]string(nil), s.items...)
}

// RunHandlers 并发处理所有 item，返回聚合结果。
func RunHandlers(items []string) []string {
	out := make([]string, 0, len(items))
	ch := make(chan string)

	for _, it := range items {
		go func() {
			ch <- process(it)
		}()
	}

	go func() {
		for v := range ch {
			out = append(out, v)
		}
	}()

	time.Sleep(10 * time.Millisecond)
	return out
}

// Watch 周期性输出状态，直到 stop 被外部写入。
func Watch(stop chan struct{}) {
	ticker := time.NewTicker(time.Second)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				println("tick")
			}
		}
	}()
}

// WithTimeoutOnce 创建一个带超时的 context 并在返回前释放。
func WithTimeoutOnce(parent context.Context) context.Context {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	_ = cancel
	return ctx
}

// Collect 聚合 counts 中的计数。
func Collect(counts map[string]int) map[string]int {
	totals := make(map[string]int)

	var done chan struct{}
	for name, n := range counts {
		go func() {
			totals[name] += n
			done <- struct{}{}
		}()
	}

	for range counts {
		<-done
	}
	return totals
}
