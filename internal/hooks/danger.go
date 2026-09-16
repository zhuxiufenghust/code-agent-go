// Package hooks 集中存放工具执行前的"安全钩子"：
// 目前提供高危命令识别（MatchDangerous），供审批（HITL）与拦截策略复用。
package hooks

import (
	"regexp"
	"strings"
	"sync"
)

// dangerRule 描述一条高危规则：命中即需要人工确认（或直接拒绝，取决于调用方策略）。
type dangerRule struct {
	// Reason 是给用户/模型看的命中原因。
	Reason string
	// Pattern 是作用于命令字符串的正则（大小写不敏感）。
	Pattern *regexp.Regexp
}

// dangerousRules 是内置的高危命令规则表。
// 与配置里的 dangerCmds 是"或"的关系：内置规则保证"开箱即有底线"，
// 配置项用于补充业务特有的高危操作。
var dangerousRules = []dangerRule{
	{Reason: "递归强制删除（rm -rf/-fr）", Pattern: regexp.MustCompile(`(?i)\brm\b\s+(-[a-zA-Z]{0,3}[rR][a-zA-Z]{0,3}[fF][a-zA-Z]{0,3}|-[a-zA-Z]{0,3}[fF][a-zA-Z]{0,3}[rR][a-zA-Z]{0,3})\b`)},
	{Reason: "强制丢弃工作区改动（git reset --hard / checkout -- .）", Pattern: regexp.MustCompile(`(?i)\bgit\b[^|;&\n]*\s+(reset\s+--hard|checkout\s+--\s+\.|clean\s+-[a-zA-Z]*f)`)},
	{Reason: "强制推送（force push）", Pattern: regexp.MustCompile(`(?i)\bgit\b[^|;&\n]*\s+push\b[^|;&\n]*(--force|-f\b|--force-with-lease)`)},
	{Reason: "破坏性数据库操作（DROP/TRUNCATE）", Pattern: regexp.MustCompile(`(?i)\b(drop\s+(table|database|schema)|truncate\s+table)\b`)},
	{Reason: "磁盘/文件系统级破坏（mkfs/dd/shred）", Pattern: regexp.MustCompile(`(?i)(\bmkfs(\.[a-z0-9]+)?\b|\bdd\s+if=|\bshred\b)`)},
	{Reason: "fork 炸弹", Pattern: regexp.MustCompile(`:[[:space:]]*\([[:space:]]*\)[[:space:]]*\{`)},
	{Reason: "远程脚本直接执行（curl|wget ... | sh/bash）", Pattern: regexp.MustCompile(`(?i)\b(curl|wget)\b[^|;\n]*\|\s*(sudo\s+)?(sh|bash|zsh)\b`)},
	{Reason: "系统级危险操作（reboot/shutdown/init 0）", Pattern: regexp.MustCompile(`(?i)\b(reboot|shutdown|init\s+0|poweroff)\b`)},
	{Reason: "开放权限（chmod 777 / chown -R）", Pattern: regexp.MustCompile(`(?i)\bchmod\b[^|;&\n]*(777|-R\s+777)`)},
}

var (
	ruleOnce   sync.Once
	ruleCache  []dangerRule
	customMu   sync.RWMutex
	customRule []dangerRule
)

// AddDangerPatterns 追加业务自定义的高危模式（正则）。
// 编译失败的模式会被忽略并跳过（不会因为一个坏正则导致整体不可用）。
func AddDangerPatterns(reason string, patterns ...string) {
	compiled := make([]dangerRule, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, dangerRule{Reason: reason, Pattern: re})
	}
	customMu.Lock()
	customRule = append(customRule, compiled...)
	customMu.Unlock()
}

// rules 返回内置规则（编译结果只初始化一次）与自定义规则的合集的快照。
func rules() []dangerRule {
	ruleOnce.Do(func() { ruleCache = dangerousRules })
	customMu.RLock()
	defer customMu.RUnlock()
	if len(customRule) == 0 {
		return ruleCache
	}
	out := make([]dangerRule, 0, len(ruleCache)+len(customRule))
	out = append(out, ruleCache...)
	out = append(out, customRule...)
	return out
}

// MatchDangerous 判断命令是否命中高危规则。
// 命中时返回 true 与可读的命中原因（用于审批弹窗/拒绝原因），否则返回 false 与空串。
// 空命令一律视为不危险，避免把"参数缺失"误判成危险操作。
func MatchDangerous(cmd string) (bool, string) {
	if strings.TrimSpace(cmd) == "" {
		return false, ""
	}
	for _, r := range rules() {
		if r.Pattern.MatchString(cmd) {
			return true, r.Reason
		}
	}
	return false, ""
}
