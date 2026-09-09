package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewBashTool_Definition(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	def := tool.GetDefinition()
	if def.Name != "bash_tool" {
		t.Errorf("期望 tool name 为 bash_tool, 实际为 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	if def.InputSchema == nil {
		t.Error("期望 input_schema 非空")
	}
	// 校验 required 包含 command
	schemaMap, ok := def.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("期望 input_schema 为 map[string]interface{}, 实际 %T", def.InputSchema)
	}
	required, ok := schemaMap["required"].([]string)
	if !ok || !contains(required, "command") {
		t.Errorf("期望 input_schema.required 包含 command, 实际 %v", schemaMap["required"])
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestBashTool_InvalidJSON(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestBashTool_SimpleEcho(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"command": "echo hello"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("期望输出包含 hello, 实际 %q", got)
	}
}

func TestBashTool_ChainedCommands(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"command": "printf a && printf b"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if got != "ab" {
		t.Errorf("期望 ab, 实际 %q", got)
	}
}

func TestBashTool_StderrCaptured(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"command": "echo errmsg >&2"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "errmsg") {
		t.Errorf("期望合并输出包含 errmsg, 实际 %q", got)
	}
}

func TestBashTool_NoOutput(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"command": "true"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	want := "命令执行成功，无终端输出。"
	if got != want {
		t.Errorf("期望 %q, 实际 %q", want, got)
	}
}

func TestBashTool_WorkDir(t *testing.T) {
	dir := writeTempFileInDir(t, "marker.txt", "data")
	tool := NewBashTool(dir)
	input, _ := json.Marshal(map[string]interface{}{"command": "ls"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "marker.txt") {
		t.Errorf("期望在 workDir 下执行 ls 能看到 marker.txt, 实际 %q", got)
	}
}

func TestBashTool_CommandError(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	input, _ := json.Marshal(map[string]interface{}{"command": "exit 3"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望非 0 退出码时返回错误")
	}
}

func TestBashTool_MaxOutputLen(t *testing.T) {
	tool := NewBashTool(t.TempDir(), WithMaxOutputLen(3))
	input, _ := json.Marshal(map[string]interface{}{"command": "echo hello"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// echo 输出 "hello\n"，截断到 3 字节应为 "hel"
	if got != "hel" {
		t.Errorf("期望截断为 hel, 实际 %q", got)
	}
}

func TestBashTool_InputTimeout(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	// 超时 1 秒，命令需要 2 秒，应被中断
	input, _ := json.Marshal(map[string]interface{}{
		"command":       "sleep 2",
		"timeout_secs":  1,
	})
	start := time.Now()
	_, err := tool.Execute(context.Background(), input)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("期望超时返回错误")
	}
	if elapsed > 5*time.Second {
		t.Errorf("超时控制似乎未生效，耗时 %v", elapsed)
	}
}

func TestBashTool_TimeoutClamp(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	// 传入远超上限的 timeout_secs，应被截断到 maxBashTimeout 而不会报错中断
	input, _ := json.Marshal(map[string]interface{}{
		"command":      "echo clamped",
		"timeout_secs": 100000,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(got, "clamped") {
		t.Errorf("期望输出 clamped, 实际 %q", got)
	}
}

func TestBashTool_WithTimeoutOption(t *testing.T) {
	tool := NewBashTool(t.TempDir(), WithTimeout(2*time.Second))
	// 命令耗时 5 秒，但默认超时只有 2 秒，应被中断
	input, _ := json.Marshal(map[string]interface{}{"command": "sleep 5"})
	start := time.Now()
	_, err := tool.Execute(context.Background(), input)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("期望 WithTimeout 设置的超时生效并返回错误")
	}
	if elapsed > 6*time.Second {
		t.Errorf("WithTimeout 似乎未生效，耗时 %v", elapsed)
	}
}

// TestBashTool_TimeoutErrorContainsKeyword 验证超时错误携带"超时"关键字，
// 以便 RecoveryManager 能识别并注入 [系统救援指南]（自愈提示）。
func TestBashTool_TimeoutErrorContainsKeyword(t *testing.T) {
	// 用 WithTimeout 设极短超时，命令实际会跑更久，必然超时
	tool := NewBashTool(t.TempDir(), WithTimeout(300*time.Millisecond))
	input, _ := json.Marshal(map[string]interface{}{"command": "sleep 2"})

	start := time.Now()
	_, err := tool.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时返回错误")
	}
	// 关键断言：错误必须包含"超时"关键字，供自愈逻辑识别
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("期望超时错误包含'超时'关键字以触发自愈提示, 实际: %q", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Errorf("超时控制似乎未生效，耗时 %v", elapsed)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	// 未设置任何超时时，应回退到 defaultBashTimeout
	base := NewBashTool(t.TempDir())
	if got := base.effectiveTimeout(0); got != defaultBashTimeout {
		t.Errorf("期望默认回退到 defaultBashTimeout(%v), 实际 %v", defaultBashTimeout, got)
	}

	// 传入合法的 timeout_secs 应优先采用
	if got := base.effectiveTimeout(10); got != 10*time.Second {
		t.Errorf("期望 10s, 实际 %v", got)
	}

	// 超出 maxBashTimeout 的 timeout_secs 不被采用，回退到默认
	if got := base.effectiveTimeout(float64(maxBashTimeout.Seconds()) + 100); got != defaultBashTimeout {
		t.Errorf("超出上限应回退默认, 实际 %v", got)
	}

	// 设置了 WithTimeout 时，0 输入应回退到该值
	withOpt := NewBashTool(t.TempDir(), WithTimeout(7*time.Second))
	if got := withOpt.effectiveTimeout(0); got != 7*time.Second {
		t.Errorf("期望回退到 WithTimeout 的 7s, 实际 %v", got)
	}

	// 即使设置了 WithTimeout，显式 timeout_secs 仍优先
	if got := withOpt.effectiveTimeout(3); got != 3*time.Second {
		t.Errorf("期望显式 3s 优先, 实际 %v", got)
	}
}
