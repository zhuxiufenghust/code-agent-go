package skill

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/zhuxiufenghust/code-agent-go/internal/log"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
	"go.uber.org/zap"
)

// 根据目录和文件格式加载Skill
type SkillLoader struct {
	homeDir  string
	workDir  string
	skillMap map[string]schema.Skill
}

const (
	skillFileName = "SKILL.md"
	referenceDir  = "references/"
	scriptDir     = "scripts/"
	assetDir      = "assets/"
	delimiter     = "---\n"
	delimiterCRLF = "---\r\n"

	UnknownSkillName = "Unknown Skill"
)

func NewSkillLoader(homeDir, workDir string) *SkillLoader {
	return &SkillLoader{
		homeDir:  homeDir,
		workDir:  workDir,
		skillMap: make(map[string]schema.Skill),
	}
}

func (l *SkillLoader) GetSkillMap() map[string]schema.Skill {
	return l.skillMap
}

func (l *SkillLoader) LoadSkills() error {
	skills, err := l.LoadSkillFromDir(l.homeDir)
	if err != nil {
		return err
	}
	skillMap := make(map[string]schema.Skill)
	for _, skill := range skills {
		skillMap[skill.Name] = skill
	}
	skills, err = l.LoadSkillFromDir(l.workDir)
	if err != nil {
		return err
	}
	for _, skill := range skills {
		if _, exists := skillMap[skill.Name]; exists {
			log.Warn("技能已存在，将被覆盖", zap.String("skillName", skill.Name))
		}
		skillMap[skill.Name] = skill
	}
	l.skillMap = skillMap
	return nil
}

func (l *SkillLoader) LoadSkillFromDir(skillDir string) ([]schema.Skill, error) {
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return nil, errors.Wrap(err, "读取技能目录失败")
	}
	var skills []schema.Skill
	for _, entry := range entries {
		if entry.IsDir() {
			skillPath := filepath.Join(skillDir, entry.Name())
			skill, err := l.LoadOneSkill(skillPath)
			if err != nil {
				log.Warn("加载技能失败", zap.String("skill_dir", skillPath), zap.Error(err))
				continue
			}
			skills = append(skills, *skill)
		}
	}
	return skills, nil
}

func (l *SkillLoader) LoadOneSkill(oneSkillDir string) (*schema.Skill, error) {
	skillFilePath := filepath.Join(oneSkillDir, skillFileName)
	if _, err := os.Stat(skillFilePath); os.IsNotExist(err) {
		return nil, errors.Wrap(err, "skill.md 不存在")
	}

	dirName := filepath.Base(filepath.Dir(skillFilePath)) // 获取技能所在目录的名称
	prompt, err := os.ReadFile(skillFilePath)
	if err != nil {
		return nil, errors.Wrap(err, "读取 skill.md 失败")
	}
	skill := l.parseSkillMD(string(prompt))
	if skill.Name == UnknownSkillName {
		return nil, errors.New("解析skill 失败")
	}
	if dirName != skill.Name {
		log.Warn("技能目录名与 skill.md 中的 name 不一致", zap.String("dirName", dirName), zap.String("skillName", skill.Name))
	}
	return skill, nil
}

func (l *SkillLoader) parseSkillMD(content string) *schema.Skill {
	skill := &schema.Skill{
		Name:        UnknownSkillName,
		Description: "No description provided.",
		Body:        content, // 默认将全量内容作为 body
	}

	// 简单解析 YAML Frontmatter (以 --- 包裹)
	if strings.HasPrefix(content, delimiter) || strings.HasPrefix(content, delimiterCRLF) {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) == 3 {
			frontmatter := parts[1]
			skill.Body = strings.TrimSpace(parts[2])
			// 逐行提取 metadata
			lines := strings.Split(frontmatter, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
		}
	}
	return skill
}
