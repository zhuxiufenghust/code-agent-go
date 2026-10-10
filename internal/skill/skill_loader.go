package skill

import (
	"io/fs"
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
	userskillDir := filepath.Join(l.homeDir, ".config/code-agent-go/skills")
	skills, err := l.LoadSkillFromDir(userskillDir)
	if err != nil {
		log.Warn("load skill from skill dir failed", zap.String("skill_dir", userskillDir))
	}
	skillMap := make(map[string]schema.Skill)
	for _, skill := range skills {
		skillMap[skill.Name] = skill
	}
	workDirSkills := filepath.Join(l.workDir, ".config/code-agent-go/skills")
	skills, err = l.LoadSkillFromDir(workDirSkills)
	if err != nil {
		log.Warn("load skill from skill dir failed", zap.String("skill_dir", workDirSkills))
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

// resourceEntry 描述技能目录下的一个可按需取用的资源文件。
// Name 是相对资源目录的路径（给 LLM 看的易读名字），
// Path 是相对技能目录的路径（配合技能的 BaseDir 由 read/bash 取用）。
type resourceEntry struct {
	Name string
	Path string
}

// loadResources 递归扫描技能目录下某类资源目录（references/ scripts/ assets/）。
// 技能可以不带资源，目录不存在属正常情况，返回 nil；其他读取错误仅告警，
// 不阻断技能加载——少一条索引远比整个技能不可用更安全。
func loadResources(resourceDir string) []resourceEntry {
	entries := make([]resourceEntry, 0)
	err := filepath.WalkDir(resourceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// 跳过隐藏文件（.DS_Store 等），它们不参与技能索引。
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(resourceDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		entries = append(entries, resourceEntry{
			Name: rel,
			Path: filepath.ToSlash(filepath.Join(filepath.Base(resourceDir), rel)),
		})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Warn("扫描技能资源失败", zap.String("resource_dir", resourceDir), zap.Error(err))
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

func toReferences(entries []resourceEntry) []schema.Reference {
	out := make([]schema.Reference, 0, len(entries))
	for _, e := range entries {
		out = append(out, schema.Reference{Name: e.Name, Path: e.Path})
	}
	return out
}

func toScripts(entries []resourceEntry) []schema.Script {
	out := make([]schema.Script, 0, len(entries))
	for _, e := range entries {
		out = append(out, schema.Script{Name: e.Name, Path: e.Path})
	}
	return out
}

func toAssets(entries []resourceEntry) []schema.Asset {
	out := make([]schema.Asset, 0, len(entries))
	for _, e := range entries {
		out = append(out, schema.Asset{Name: e.Name, Path: e.Path})
	}
	return out
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

	// BaseDir 记录技能目录的绝对路径：资源 Path 是相对它的，
	// 没有它，模型拿到 references/xxx.md 也不知道该去哪里读。
	if abs, err := filepath.Abs(oneSkillDir); err == nil {
		skill.BaseDir = abs
	} else {
		skill.BaseDir = oneSkillDir
	}

	// 资源索引：正文里通常只说明"如何做"，references/ scripts/ assets/ 下才是
	// 具体素材。扫出来交给 Skill，让模型在拿到正文后能按需（读其中一份）继续取用，
	// 而不是一上来就把所有素材塞进上下文。
	skill.References = toReferences(loadResources(filepath.Join(oneSkillDir, referenceDir)))
	skill.Scripts = toScripts(loadResources(filepath.Join(oneSkillDir, scriptDir)))
	skill.Assets = toAssets(loadResources(filepath.Join(oneSkillDir, assetDir)))
	return skill, nil
}

// yamlScalar 提取 YAML 标量字面量：去空白并剥掉成对的外层引号。
// frontmatter 里为了让值包含 '、"、冒号等特殊字符，常写成 name: "..."，
// 若不去引号，这些引号会一路进到工具描述里，让模型看到一串多余的转义。
func yamlScalar(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 {
		for _, q := range []byte{'"', '\''} {
			if v[0] == q && v[len(v)-1] == q {
				return v[1 : len(v)-1]
			}
		}
	}
	return v
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
					skill.Name = yamlScalar(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					skill.Description = yamlScalar(strings.TrimPrefix(line, "description:"))
				}
			}
		}
	}
	return skill
}
