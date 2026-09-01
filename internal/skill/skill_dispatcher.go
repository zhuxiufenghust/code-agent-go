package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

type SkillDispatcher struct {
	skillLoader    *SkillLoader
	toolDefinition schema.ToolDefinition
}

var skillDescriptionPrefix = "技能分发器，支持以下技能:\n"

func NewSkillDispatcher(skillLoader *SkillLoader) *SkillDispatcher {
	dispatcher := &SkillDispatcher{
		skillLoader: skillLoader,
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

	// 这里可以根据 skill 的定义执行相应的操作
	// 例如调用 skill 的 Execute 方法，或者返回 skill 的信息等
	return "技能 " + skill.Name + " 已成功分发", nil
}
