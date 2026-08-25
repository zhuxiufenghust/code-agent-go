package logfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// MaxOutputLen 日志中单条输出的最大字节数。超出部分被截断并附加提示。
// 防止单条工具/系统输出撑爆日志文件或终端缓冲区。
const MaxOutputLen = 512

// Indent 块状日志续行（Continuation Line）的统一缩进。
// 其他模块若编写多行日志，应使用此常量保持视觉对齐。
const Indent = "        "

// argInlineThreshold 当 JSON 压缩后长度小于该阈值时以单行内联展示；否则切换为多行 pretty-print。
const argInlineThreshold = 80

// FormatMsg 渲染通用单行日志条目：[prefix] msg。
func FormatMsg(prefix, msg string) string {
	return fmt.Sprintf("[%s] %s", prefix, msg)
}

func encodeJSONNoEscape(buf *bytes.Buffer, raw json.RawMessage, indent bool) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(v)
}

func FormatJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}

	// Compact 阶段同样需关闭 HTML escape，否则短 payload 也会出现 &。
	// 注意：json.Encoder.Encode 总会附加一个尾随换行符，需手动 TrimRight。
	var compact bytes.Buffer
	if err := encodeJSONNoEscape(&compact, raw, false); err != nil {
		return string(raw)
	}
	compactStr := strings.TrimRight(compact.String(), "\n")
	if len(compactStr) <= argInlineThreshold {
		return compactStr
	}

	var pretty bytes.Buffer
	if err := encodeJSONNoEscape(&pretty, raw, true); err != nil {
		return compactStr
	}
	// json.Encoder 的 Indent 不会缩进首行，需手动补齐
	indented := strings.ReplaceAll(strings.TrimRight(pretty.String(), "\n"), "\n", "\n"+Indent+"  ")
	return Indent + "  " + indented
}
func FormatMsgs(msgs []schema.Message) string {
	var sb strings.Builder
	for _, msg := range msgs {
		b, _ := json.Marshal(msg)
		sb.WriteString(string(b))
		sb.WriteString(" ")
	}
	return sb.String()
}
