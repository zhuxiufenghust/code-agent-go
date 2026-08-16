package provider

import (
	"sort"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// toolCallAccumulator 缓存单个工具调用在流式过程中分片到达的字段。
// Arguments 使用 strings.Builder 增量拼接，避免反复内存分配。
type toolCallAccumulator struct {
	index int
	id    string
	name  string
	args  strings.Builder
}

// toolCallAccumulators 按 Index 组织的累积器集合，供 OpenAI/Anthropic 流式 Provider 复用。
// 同一流中可能同时存在多个并行工具调用（Parallel Tool Calling），各自通过 Index 隔离。
type toolCallAccumulators map[int]*toolCallAccumulator

// newToolCallAccumulators 创建空的累积器集合。
func newToolCallAccumulators() toolCallAccumulators {
	return make(toolCallAccumulators)
}

// get 返回 idx 对应的累积器，不存在时按需创建并写入集合。
func (a toolCallAccumulators) get(idx int) *toolCallAccumulator {
	if acc, ok := a[idx]; ok {
		return acc
	}
	acc := &toolCallAccumulator{index: idx}
	a[idx] = acc
	return acc
}

// start 在流式响应首次出现某工具调用时记录 ID 和 Name。
// 重复调用相同 idx 会覆盖旧值（理论上不会发生，由 Provider 保证只在 *_start 时调用）。
func (a toolCallAccumulators) start(idx int, id, name string) {
	acc := a.get(idx)
	acc.id = id
	acc.name = name
}

// appendArgs 把参数 JSON 的部分片段追加到 idx 对应累积器的缓冲区。
func (a toolCallAccumulators) appendArgs(idx int, partial string) {
	a.get(idx).args.WriteString(partial)
}

// finalize 按 Index 升序重组累积结果为 ToolCall 列表，供 StreamChunkDone 携带。
// 返回 nil 表示流中没有工具调用（不应作为错误处理）。
//
// 关键修正：先收集实际存在的 key 再升序遍历，而非假设 key 从 0 连续。
// Anthropic 流中 index 是 content-block 序号——开启 extended thinking 或有正文时，
// thinking/text 块占据 index 0，tool_use 块从 1 起，key 集合形如 {1,2,3} 而非 {0,1,2}。
// 旧实现 `for i:=0;i<len(a);i++` 会因 a[0] 不存在而漏掉末尾的工具调用，
// 导致模型实际发出的工具调用被静默丢弃。
func (a toolCallAccumulators) finalize() []schema.ToolCall {
	if len(a) == 0 {
		return nil
	}
	keys := make([]int, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	result := make([]schema.ToolCall, 0, len(a))
	for _, k := range keys {
		acc := a[k]
		result = append(result, schema.ToolCall{
			ID:        acc.id,
			Name:      acc.name,
			Arguments: []byte(acc.args.String()),
		})
	}
	return result
}
