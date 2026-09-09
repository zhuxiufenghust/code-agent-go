package context_mng

import (
	"context"
	"fmt"
	"strings"
)

type RecoveryManager struct {
	// Add fields for RecoveryManager if needed
}

// NewRecoveryManager creates a new instance of RecoveryManager.
func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject is a placeholder method for analyzing and injecting context.
// Implement the actual recovery logic as needed.
func (rm *RecoveryManager) AnalyzeAndInject(ctx context.Context, toolName string, rawErr string) string {
	// Placeholder implementation
	var hint string
	// 我们使用相对稳定的英文系统级报错关键字，或者我们自己手写的工具内部固定报错格式
	lowerError := strings.ToLower(rawErr)
	// 匹配我们在 07 讲中手写的 fuzzyReplace 的固定报错抛出
	switch toolName {
	case "edit_tool":
		if strings.Contains(lowerError, "在文件中未找到 old_text") || strings.Contains(lowerError, "找不到该代码片段") {
			hint = "你提供的 old_text 与文件当前内容不一致，或者缺少必要的缩进。请先使用 `read_file` 工具重新读取该文件，获取最新、准确的内容后，再重新发起编辑。"
		} else if strings.Contains(lowerError, "匹配到了") || strings.Contains(lowerError, "上下文代码") || strings.Contains(lowerError, "上下行代码") {
			hint = "你的 old_text 不够具体，命中了多个相同代码块。请在 old_text 中增加上下相邻的几行代码，以确保替换的唯一性。"
		} else if strings.Contains(lowerError, "文件不存在") || strings.Contains(lowerError, "no such file or directory") {
			hint = "目标文件不存在。请不要凭空猜测路径，先使用 `bash` 执行 `ls -la` 或 `find . -name` 确认文件位置后再编辑。"
		}
	case "read_tool", "write_tool":
		if strings.Contains(lowerError, "no such file or directory") || strings.Contains(lowerError, "文件不存在") {
			hint = "路径似乎不正确。请不要凭空猜测，先使用 `bash` 执行 `ls -la` 或 `find . -name` 命令查找正确的目录结构和文件名。"
		} else if strings.Contains(lowerError, "permission denied") {
			hint = "你没有权限操作该文件。请检查工作区限制，或者思考是否需要修改其他文件。"
		}
	case "bash_tool":
		if strings.Contains(lowerError, "command not found") {
			hint = "系统中未安装该命令。请先思考：是否有替代命令？或者你需要先编写脚本进行安装？"
		} else if strings.Contains(lowerError, "超时") || strings.Contains(lowerError, "deadline exceeded") { // 匹配 context.WithTimeout 的真实报错 "context deadline exceeded"
			hint = "该命令执行被超时强杀。如果它是一个常驻服务（如 server 或 watch），请将其转入后台执行（例如使用 `nohup ... &`），不要阻塞主线程。"
		} else if strings.Contains(lowerError, "syntax error") {
			hint = "Bash 语法错误。请检查引号转义或特殊字符，确保命令在终端中可直接运行。"
		}
	}
	// 如果没有匹配到特定特征，原样返回原始错误； // 如果匹配到了，拼接成强有力的、带有浓厚“系统指导意味”的行动指南。
	if hint == "" {
		return rawErr
	}
	return fmt.Sprintf("%s\n\n[系统救援指南]: %s", rawErr, hint)
}
