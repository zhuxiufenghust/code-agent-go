package tools

import (
	"path/filepath"
	"strings"
)

// resolvePath 将工具收到的路径解析为 workDir 下的绝对路径，避免重复拼接 workDir。
//
// 模型可能传入三种形式：
//   - 相对路径（如 "cmd/main.go"）：直接拼到 workDir 下；
//   - 绝对路径（如 "/abs/cmd/main.go"）：filepath.Join 会原样返回，直接用；
//   - 漏了前导斜杠、但内容里已带 workDir 前缀的相对路径
//     （如 "Users/.../code-agent-go/cmd/main.go"）：若不处理会被 workDir 再拼一次，
//     得到 "workDir/workDir/cmd/main.go" 这种错误路径。这里识别并剥掉前缀后再拼。
func resolvePath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	// 模型可能漏了前导 /，导致 p 形如 "Users/.../code-agent-go/cmd/main.go"
	// （内容里已带 workDir 前缀）。这里把"带 / 或不带 / 的 workDir 前缀"都剥掉再拼。
	stripped := p
	if strings.HasPrefix(stripped, workDir) {
		stripped = strings.TrimPrefix(stripped, workDir)
	} else if noSlash := strings.TrimPrefix(workDir, "/"); strings.HasPrefix(stripped, noSlash) {
		stripped = strings.TrimPrefix(stripped, noSlash)
	}
	stripped = strings.TrimPrefix(stripped, "/")
	return filepath.Join(workDir, stripped)
}
