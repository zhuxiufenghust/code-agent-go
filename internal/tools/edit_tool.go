package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

type EditTool struct {
	schema.ToolDefinition
	workDir string
}
type EditToolOption func(*EditTool)

type editInput struct {
	FilePath   string `json:"file_path"`
	SourceText string `json:"source_text"`
	TargetText string `json:"target_text"`
}

// editContextLines 是改动上下文中前后各保留的行数。
const editContextLines = 3

func NewEditTool(workDir string, options ...EditToolOption) *EditTool {
	tool := &EditTool{
		ToolDefinition: schema.ToolDefinition{
			Name:        "edit_tool",
			Description: "编辑指定路径的文件内容。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "要编辑的文件的路径, 如 src/main.py",
					},
					"source_text": map[string]interface{}{
						"type":        "string",
						"description": "要编辑的原始代码",
					},
					"target_text": map[string]interface{}{
						"type":        "string",
						"description": "要编辑的目标代码",
					},
				},
			},
		},
	}
	tool.workDir = workDir
	for _, opt := range options {
		opt(tool)
	}
	return tool
}

func (t *EditTool) GetDefinition() schema.ToolDefinition {
	return t.ToolDefinition
}

func (t *EditTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var editInput editInput
	if err := json.Unmarshal(input, &editInput); err != nil {
		return "", fmt.Errorf("解析输入参数失败: %w", err)
	}
	fullFilePath := filepath.Join(t.workDir, editInput.FilePath)
	if _, err := os.Stat(fullFilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("文件不存在: %s", fullFilePath)
	}
	content, err := os.ReadFile(fullFilePath)
	if err != nil {
		return "", fmt.Errorf("读取文件 %s 失败: %w", fullFilePath, err)
	}
	originalContent := string(content)

	newContent, err := fuzzyReplace(originalContent, editInput.SourceText, editInput.TargetText)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(fullFilePath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("写回文件失败：%w", err)
	}
	return buildEditSummary(editInput.FilePath, originalContent, newContent), nil
}

// fuzzyReplace 实现了四级容错降级替换算法
func fuzzyReplace(originalContent, oldText, newText string) (string, error) {
	// L1: 精确匹配
	count := strings.Count(originalContent, oldText)
	if count == 1 {
		return strings.Replace(originalContent, oldText, newText, 1), nil
	}
	if count > 1 {
		return "", fmt.Errorf("old_text 匹配到了 %d 处，请提供更多的上下文代码以确保唯一性", count)
	}

	// L2: 换行符归一化 (统一将 \r\n 转换为 \n)
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")

	count = strings.Count(normalizedContent, normalizedOld)
	if count == 1 {
		return strings.Replace(normalizedContent, normalizedOld, newText, 1), nil
	}

	// L3: Trim Space 匹配 (忽略首尾的空行和空格)
	trimmedOld := strings.TrimSpace(normalizedOld)
	if trimmedOld != "" {
		count = strings.Count(normalizedContent, trimmedOld)
		if count == 1 {
			// 注意：这里替换时，我们只能替换被 Trim 后的部分，不能直接用 newText 破坏原本的缩进
			// 为了保持本专栏代码不过于冗长复杂，当触发 L3/L4 时，如果 newText 没有带有正确的缩进，
			// 可能会导致替换后代码格式不美观。但这总比直接报错让 Agent 死循环要好。
			return strings.Replace(normalizedContent, trimmedOld, newText, 1), nil
		}
	}

	// L4: 逐行去缩进匹配 (最强力的容错：消除大模型遗漏缩进的幻觉)
	return lineByLineReplace(normalizedContent, normalizedOld, newText)
}

// lineByLineReplace 将文本按行切割，去除首尾空白后进行滑动窗口匹配
func lineByLineReplace(content, oldText, newText string) (string, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimSpace(oldText), "\n")

	if len(oldLines) == 0 || len(contentLines) < len(oldLines) {
		return "", fmt.Errorf("找不到该代码片段")
	}

	// 清理 oldLines 的每行首尾空白
	for i := range oldLines {
		oldLines[i] = strings.TrimSpace(oldLines[i])
	}

	matchCount := 0
	matchStartIndex := -1
	matchEndIndex := -1

	// 滑动窗口在原始文件中寻找匹配块
	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		isMatch := true
		for j := 0; j < len(oldLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != oldLines[j] {
				isMatch = false
				break
			}
		}

		if isMatch {
			matchCount++
			matchStartIndex = i
			matchEndIndex = i + len(oldLines)
		}
	}

	if matchCount == 0 {
		return "", fmt.Errorf("在文件中未找到 old_text，请大模型先调用 read_file 仔细确认文件内容和缩进")
	}
	if matchCount > 1 {
		return "", fmt.Errorf("模糊匹配到了 %d 处相似代码，请提供更多上下行代码以精确定位", matchCount)
	}

	// 执行替换：将匹配到的原始行范围替换为 newText 拆分后的行
	// (这里简单处理，将 newText 直接作为整体替换进去)
	var newContentLines []string
	newContentLines = append(newContentLines, contentLines[:matchStartIndex]...)
	newContentLines = append(newContentLines, newText) // 插入新内容
	newContentLines = append(newContentLines, contentLines[matchEndIndex:]...)

	return strings.Join(newContentLines, "\n"), nil
}

func buildEditSummary(path, originalContent, newContent string) string {
	// 归一化换行符，保证跨平台比较一致
	orig := strings.ReplaceAll(originalContent, "\r\n", "\n")
	next := strings.ReplaceAll(newContent, "\r\n", "\n")

	// trimTrailingEmpty 去掉 strings.Split 在文件末尾 \n 后产生的空串元素，
	// 而非用 TrimRight 吃掉所有尾部换行——后者会丢失有意义的末尾空行。
	trimTrailingEmpty := func(lines []string) []string {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			return lines[:len(lines)-1]
		}
		return lines
	}
	origLines := trimTrailingEmpty(strings.Split(orig, "\n"))
	nextLines := trimTrailingEmpty(strings.Split(next, "\n"))

	// 找到第一处差异的行（公共前缀长度）
	start := 0
	for start < len(origLines) && start < len(nextLines) && origLines[start] == nextLines[start] {
		start++
	}

	// 从尾部向前找公共后缀的起始位置
	origEnd := len(origLines) - 1
	nextEnd := len(nextLines) - 1
	for origEnd >= start && nextEnd >= start && origLines[origEnd] == nextLines[nextEnd] {
		origEnd--
		nextEnd--
	}

	// 计算删除/新增行数（纯插入或纯删除时对应计数为 0）
	removed := origEnd - start + 1
	if removed < 0 {
		removed = 0
	}
	added := nextEnd - start + 1
	if added < 0 {
		added = 0
	}

	// 变更超过 20 行只报数字，避免输出过长
	if removed+added > 20 {
		return fmt.Sprintf("成功修改文件: %s（删除 %d 行，新增 %d 行）", path, removed, added)
	}

	ctxStart := max(0, start-editContextLines)
	ctxEnd := min(len(origLines)-1, origEnd+editContextLines)

	var sb strings.Builder
	fmt.Fprintf(&sb, "成功修改文件: %s\n\n--- 改动上下文 ---\n", path)
	// 改动前的上下文行
	for i := ctxStart; i < start; i++ {
		fmt.Fprintf(&sb, "  %s\n", origLines[i])
	}
	// 被删除的行
	for i := start; i <= origEnd; i++ {
		fmt.Fprintf(&sb, "- %s\n", origLines[i])
	}
	// 新增的行
	for i := start; i <= nextEnd; i++ {
		fmt.Fprintf(&sb, "+ %s\n", nextLines[i])
	}
	// 改动后的上下文行（取 original 后续行）
	for i := origEnd + 1; i <= ctxEnd; i++ {
		fmt.Fprintf(&sb, "  %s\n", origLines[i])
	}

	// 仅声明"字节已写入"，不暗示"行为已验证"：避免抑制后续的真实行为验证（自愈的关键反馈）。
	fmt.Fprint(&sb, "---\n✓ 精确匹配，以上 diff 已确认字节写入，无需再 grep/read_file 确认本次改动是否落地。"+
		"但这只代表「写入成功」，不代表行为正确——请运行真实测试或复现脚本来验证修复行为。")
	return sb.String()
}
