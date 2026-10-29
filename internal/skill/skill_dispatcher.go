package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

// skillMaxOutputSize 是技能正文的输出预算(字节)，高于工具的通用上限。
// SKILL.md 通常是完整的流程规范，天然比命令/文件输出长；被 20KB 通用上限
// 切掉会让模型收到残缺的步骤说明，却又无从察觉。超出该预算说明技能该拆分了。
const skillMaxOutputSize = 64 * 1024

type SkillDispatcher struct {
	skillLoader    *SkillLoader
	toolDefinition schema.ToolDefinition

	mu sync.Mutex
	// loaded 记录正文已注入过上下文的技能，用于幂等：
	// 正文一旦进入上下文就一直在那里，重复注入只会白白占用窗口。
	loaded map[string]struct{}
}

// MaxOutputSize 实现 tools.ToolOutputBudget，让技能正文使用自己的输出预算，
// 而不是被迫套用面向命令/文件输出的通用截断上限。
func (d *SkillDispatcher) MaxOutputSize() int { return skillMaxOutputSize }

var skillDescriptionPrefix = "技能分发器，支持以下技能:\n"

// skillUsageHint 告诉模型"技能要先加载、且按需加载"，而不是启动即全量注入。
const skillUsageHint = "用法:任务匹配上述某个技能时,先用本工具传入 skill_name 加载其完整说明到上下文,再按说明执行;技能附带的资源可用 read/bash 按给出的路径取用。"

func NewSkillDispatcher(skillLoader *SkillLoader) *SkillDispatcher {
	dispatcher := &SkillDispatcher{
		skillLoader: skillLoader,
		loaded:      make(map[string]struct{}),
		toolDefinition: schema.ToolDefinition{
			Name:        "skill_dispatcher",
			Description: "", //后续根据skillLoader加载的技能动态生成描述
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"skill_name": map[string]interface{}{
						"type":        "string",
						"description": "需要使用的技能名称",
					},
				},
			},
		},
	}

	dispatcher.skillLoader.LoadSkills() // 预加载技能，确保 toolDefinition.Description 能够正确生成
	return dispatcher
}

func (d *SkillDispatcher) GetDefinition() schema.ToolDefinition {
	d.mu.Lock()
	defer d.mu.Unlock()

	skills := d.skillLoader.GetSkillMap()
	if len(skills) == 0 {
		d.toolDefinition.Description = "没有可用的技能。"
		return d.toolDefinition
	}

	// 按名称排序，保证每次生成的技能列表顺序稳定（map 遍历顺序不确定）。
	names := make([]string, 0, len(skills))
	for name := range skills {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(skillDescriptionPrefix)
	for _, name := range names {
		skill := skills[name]
		sb.WriteString(fmt.Sprintf("- %s: %s\n", skill.Name, skill.Description))
	}
	sb.WriteString(skillUsageHint)
	d.toolDefinition.Description = sb.String()
	log.Info("Updated tool definition description: ", zap.String("description", d.toolDefinition.Description))
	return d.toolDefinition
}

// DispatchSkill 根据技能名称从已加载的技能表中查找对应技能。
// 找不到时返回明确错误，便于调用方（Execute）向上暴露给 LLM 以触发自愈。
func (d *SkillDispatcher) DispatchSkill(name string) (*schema.Skill, error) {
	skills := d.skillLoader.GetSkillMap()
	skill, ok := skills[name]
	if !ok {
		return nil, fmt.Errorf("技能 %s 不存在", name)
	}
	return &skill, nil
}

func (d *SkillDispatcher) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", errors.Wrap(err, "解析输入参数失败")
	}

	skill, err := d.DispatchSkill(params.SkillName)
	if err != nil {
		return "", errors.Wrap(err, "技能分发失败")
	}

	// 幂等：正文首次进入上下文后一直留在对话里，重复加载只会挤占窗口。
	if !d.markLoaded(skill.Name) {
		return buildSkillReloadNotice(skill), nil
	}
	return buildSkillContent(skill), nil
}

// buildSkillReloadNotice 生成重复加载时的简短回执。
// 仍附带资源索引（很短且可能有用），但不再重复注入正文。
func buildSkillReloadNotice(skill *schema.Skill) string {
	notice := fmt.Sprintf("[技能 %s 的正文已在上下文中,无需重复加载]", skill.Name)
	if index := formatResourceIndex(skill); index != "" {
		return notice + "\n\n" + index
	}
	return notice
}

// markLoaded 记录技能正文已注入，返回 true 表示本次是首次加载。
func (d *SkillDispatcher) markLoaded(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, loaded := d.loaded[name]; loaded {
		return false
	}
	d.loaded[name] = struct{}{}
	return true
}

// buildSkillContent 拼装技能的完整交付内容：正文 + 可按需取用的资源索引。
//
// 这里必须把 Body 真正返回给调用方，而不是回一句"已分发"：
// 工具结果会作为 tool result 回灌进对话，模型据此行动，
// 若拿不到正文，技能就只是个名字，无法驱动任何行为。
func buildSkillContent(skill *schema.Skill) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[已加载技能 %s]\n%s", skill.Name, skill.Body)
	if index := formatResourceIndex(skill); index != "" {
		sb.WriteString("\n\n" + index)
	}
	return sb.String()
}

// skillResourcePath 把资源的相对路径解析成模型可直接使用的路径。
// 有 BaseDir 时返回绝对路径——只有相对路径的话，模型明知有 checklist.md 也打不开。
func skillResourcePath(skill *schema.Skill, rel string) string {
	if skill.BaseDir == "" {
		return rel
	}
	return filepath.Join(skill.BaseDir, rel)
}

// skillResource 是技能资源在索引输出前的统一表示。
type skillResource struct {
	kind string // references/ scripts/ assets/
	name string
	path string
}

func resourcesOf(skill *schema.Skill) []skillResource {
	out := make([]skillResource, 0)
	for _, r := range skill.References {
		out = append(out, skillResource{referenceDir, r.Name, r.Path})
	}
	for _, s := range skill.Scripts {
		out = append(out, skillResource{scriptDir, s.Name, s.Path})
	}
	for _, a := range skill.Assets {
		out = append(out, skillResource{assetDir, a.Name, a.Path})
	}
	return out
}

// formatResourceIndex 输出资源索引，告诉模型下一步还能读什么、路径是什么。
// 没有任何资源时返回空串，由调用方决定是否拼接。
func formatResourceIndex(skill *schema.Skill) string {
	resources := resourcesOf(skill)
	if len(resources) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("该技能附带以下资源，需要时可用 read/bash 工具按需取用：\n")
	if skill.BaseDir != "" {
		sb.WriteString("技能目录: " + skill.BaseDir + "\n")
	}
	// 按类别分组输出，保证多次调用的结果稳定。
	for _, kind := range []string{referenceDir, scriptDir, assetDir} {
		var lines []string
		for _, r := range resources {
			if r.kind != kind {
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s (%s)", r.name, skillResourcePath(skill, r.path)))
		}
		if len(lines) == 0 {
			continue
		}
		sb.WriteString(kind + "\n")
		for _, l := range lines {
			sb.WriteString(l + "\n")
		}
	}
	return sb.String()
}
