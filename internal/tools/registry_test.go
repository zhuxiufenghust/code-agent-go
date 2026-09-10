package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// fakeTool 是一个可配置输出的测试用工具,用于验证 registry 的截断行为。
type fakeTool struct {
	name   string
	output string
	err    error
}

func (f *fakeTool) GetDefinition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: f.name}
}

func (f *fakeTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return f.output, f.err
}

func newTestRegistry(t *testing.T, maxSize int, tools ...*fakeTool) Registry {
	t.Helper()
	var opts []RegistryOption
	if maxSize > 0 {
		opts = append(opts, WithMaxOutputSize(maxSize))
	}
	r := NewRegistry(opts...)
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			t.Fatalf("register tool %s failed: %v", tool.name, err)
		}
	}
	return r
}

// 在限长内的输出不应被改动。
func TestRegistry_Execute_OutputUnderLimitUnchanged(t *testing.T) {
	out := strings.Repeat("a", 100)
	r := newTestRegistry(t, 0, &fakeTool{name: "read_tool", output: out})

	res := r.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "read_tool", Arguments: json.RawMessage(`{}`)})

	if res.IsError {
		t.Fatalf("unexpected IsError: %v", res.Output)
	}
	if res.Output != out {
		t.Fatalf("output should be unchanged, got len %d want %d", len(res.Output), len(out))
	}
	if strings.Contains(res.Output, "已截断") {
		t.Fatalf("under-limit output must not be truncated, got: %s", res.Output)
	}
}

// 超长输出应被截断,并携带截断标记与字节总数。
func TestRegistry_Execute_OutputOverLimitTruncated(t *testing.T) {
	limit := 50
	out := strings.Repeat("x", 200)
	r := newTestRegistry(t, limit, &fakeTool{name: "read_tool", output: out})

	res := r.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "read_tool", Arguments: json.RawMessage(`{}`)})

	if res.IsError {
		t.Fatalf("unexpected IsError: %v", res.Output)
	}
	if len(res.Output) > limit+len("\n...[已截断,共 200 字节]\n")+len("输出过长,请用 start_line/end_line 或 offset/limit 重新读取所需片段。") {
		t.Fatalf("output exceed expected truncated length: %d", len(res.Output))
	}
	if !strings.Contains(res.Output, "...[已截断,共 200 字节]") {
		t.Fatalf("missing truncation marker, got: %s", res.Output)
	}
	// 保留的前缀必须完整。
	if !strings.HasPrefix(res.Output, strings.Repeat("x", limit)) {
		t.Fatalf("truncated output must keep the prefix, got: %s", res.Output)
	}
}

// 不同工具应给出与其语义匹配的截断提示。
func TestRegistry_Execute_TruncationHintByTool(t *testing.T) {
	cases := []struct {
		tool     string
		wantHint string
	}{
		{"read_tool", "请用 start_line/end_line 或 offset/limit 重新读取所需片段"},
		{"bash_tool", "建议用 head/tail/grep/sed 过滤"},
		{"web_fetch_tool", "建议缩小范围或改用更精确的 query"},
		{"web_search_tool", "建议缩小范围或改用更精确的 query"},
		{"unknown_tool", "请缩小请求范围或分多次获取"},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			out := strings.Repeat("y", 300)
			r := newTestRegistry(t, 10, &fakeTool{name: c.tool, output: out})
			res := r.Execute(context.Background(), schema.ToolCall{ID: "1", Name: c.tool, Arguments: json.RawMessage(`{}`)})
			if !strings.Contains(res.Output, c.wantHint) {
				t.Fatalf("tool %s: want hint %q, got: %s", c.tool, c.wantHint, res.Output)
			}
		})
	}
}

// 工具返回 error 时,错误信息同样应被截断(避免超长错误撑爆上下文)。
func TestRegistry_Execute_ErrorOutputAlsoTruncated(t *testing.T) {
	limit := 20
	errMsg := strings.Repeat("e", 500)
	r := newTestRegistry(t, limit, &fakeTool{name: "bash_tool", err: fmt.Errorf("%s", errMsg)})

	res := r.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "bash_tool", Arguments: json.RawMessage(`{}`)})

	if !res.IsError {
		t.Fatalf("expected IsError=true, got output: %s", res.Output)
	}
	if !strings.Contains(res.Output, "...[已截断,共 500 字节]") {
		t.Fatalf("error output should be truncated, got: %s", res.Output)
	}
	if !strings.Contains(res.Output, "建议用 head/tail/grep/sed 过滤") {
		t.Fatalf("error truncation should also carry tool-specific hint, got: %s", res.Output)
	}
}

// WithMaxOutputSize 应覆盖默认上限。
func TestRegistry_WithMaxOutputSize(t *testing.T) {
	out := strings.Repeat("z", 100)
	// 默认 20KB,不会被截断。
	rDefault := newTestRegistry(t, 0, &fakeTool{name: "read_tool", output: out})
	if strings.Contains(rDefault.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "read_tool", Arguments: json.RawMessage(`{}`)}).Output, "已截断") {
		t.Fatalf("default limit must not truncate 100-byte output")
	}
	// 显式设为 10,应被截断。
	rSmall := newTestRegistry(t, 10, &fakeTool{name: "read_tool", output: out})
	if !strings.Contains(rSmall.Execute(context.Background(), schema.ToolCall{ID: "1", Name: "read_tool", Arguments: json.RawMessage(`{}`)}).Output, "已截断") {
		t.Fatalf("WithMaxOutputSize(10) must truncate 100-byte output")
	}
}
