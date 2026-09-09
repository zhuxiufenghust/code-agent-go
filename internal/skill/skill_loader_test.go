package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillLoader_NewAndEmpty(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	if got := loader.GetSkillMap(); len(got) != 0 {
		t.Errorf("期望初始技能表为空, 实际 %d 个", len(got))
	}
}

func TestSkillLoader_LoadSkills_Valid(t *testing.T) {
	homeDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "做foo")
	writeSkillDir(t, homeDir, "bar", "做bar")

	loader := NewSkillLoader(homeDir, t.TempDir())
	if err := loader.LoadSkills(); err != nil {
		t.Fatalf("LoadSkills 失败: %v", err)
	}
	skills := loader.GetSkillMap()
	if len(skills) != 2 {
		t.Fatalf("期望加载 2 个技能, 实际 %d 个", len(skills))
	}
	if skills["foo"].Description != "做foo" {
		t.Errorf("foo 描述不符: %q", skills["foo"].Description)
	}
	if skills["bar"].Description != "做bar" {
		t.Errorf("bar 描述不符: %q", skills["bar"].Description)
	}
}

func TestSkillLoader_LoadSkills_WorkDirOverridesHome(t *testing.T) {
	homeDir := t.TempDir()
	workDir := t.TempDir()
	writeSkillDir(t, homeDir, "foo", "home版")
	writeSkillDir(t, workDir, "foo", "work版") // 同名，应覆盖

	loader := NewSkillLoader(homeDir, workDir)
	if err := loader.LoadSkills(); err != nil {
		t.Fatalf("LoadSkills 失败: %v", err)
	}
	skills := loader.GetSkillMap()
	if len(skills) != 1 {
		t.Fatalf("期望同名只保留 1 个, 实际 %d 个", len(skills))
	}
	if skills["foo"].Description != "work版" {
		t.Errorf("期望 workDir 覆盖 homeDir, 实际 %q", skills["foo"].Description)
	}
}

func TestSkillLoader_LoadSkills_MissingDir(t *testing.T) {
	// homeDir 指向不存在的目录，LoadSkillFromDir 应返回错误
	loader := NewSkillLoader(filepath.Join(t.TempDir(), "no_such"), t.TempDir())
	if err := loader.LoadSkills(); err == nil {
		t.Fatal("期望读取不存在目录时返回错误")
	}
}

func TestSkillLoader_LoadOneSkill_Valid(t *testing.T) {
	base := t.TempDir()
	writeSkillDir(t, base, "foo", "做foo")
	loader := NewSkillLoader(base, t.TempDir())

	skill, err := loader.LoadOneSkill(filepath.Join(base, "foo"))
	if err != nil {
		t.Fatalf("加载技能失败: %v", err)
	}
	if skill.Name != "foo" {
		t.Errorf("期望 Name=foo, 实际 %q", skill.Name)
	}
	if skill.Description != "做foo" {
		t.Errorf("期望 Description=做foo, 实际 %q", skill.Description)
	}
	if !strings.Contains(skill.Body, "body") {
		t.Errorf("期望 Body 包含 frontmatter 之后的内容, 实际 %q", skill.Body)
	}
}

func TestSkillLoader_LoadOneSkill_MissingFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "empty")
	os.MkdirAll(dir, 0o755) // 目录存在但没有 SKILL.md
	loader := NewSkillLoader(base, t.TempDir())

	if _, err := loader.LoadOneSkill(dir); err == nil {
		t.Fatal("期望缺少 SKILL.md 时返回错误")
	}
}

func TestSkillLoader_LoadOneSkill_NameMismatchWarns(t *testing.T) {
	// 目录名与 frontmatter 中的 name 不一致，应仍能加载（仅告警）
	base := t.TempDir()
	dir := filepath.Join(base, "dir_name")
	os.MkdirAll(dir, 0o755)
	content := "---\nname: real_name\ndescription: d\n---\nbody\n"
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)
	loader := NewSkillLoader(base, t.TempDir())

	skill, err := loader.LoadOneSkill(dir)
	if err != nil {
		t.Fatalf("目录名与 name 不一致不应失败: %v", err)
	}
	if skill.Name != "real_name" {
		t.Errorf("期望使用 frontmatter 中的 name, 实际 %q", skill.Name)
	}
}

func TestParseSkillMD_ValidFrontmatter(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "---\nname: alpha\ndescription: 描述\n---\n这是正文\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != "alpha" {
		t.Errorf("Name 解析错误: %q", skill.Name)
	}
	if skill.Description != "描述" {
		t.Errorf("Description 解析错误: %q", skill.Description)
	}
	if skill.Body != "这是正文" {
		t.Errorf("Body 解析错误: %q", skill.Body)
	}
}

func TestParseSkillMD_CRLF(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "---\r\nname: beta\r\ndescription: d2\r\n---\r\nbody\r\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != "beta" {
		t.Errorf("CRLF 下 Name 解析错误: %q", skill.Name)
	}
	if skill.Body != "body" {
		t.Errorf("CRLF 下 Body 解析错误: %q", skill.Body)
	}
}

func TestParseSkillMD_NoFrontmatter(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "纯文本，没有 frontmatter"
	skill := loader.parseSkillMD(content)
	if skill.Name != UnknownSkillName {
		t.Errorf("无 frontmatter 时 Name 应为 UnknownSkillName, 实际 %q", skill.Name)
	}
	if skill.Body != content {
		t.Errorf("无 frontmatter 时 Body 应为全文, 实际 %q", skill.Body)
	}
}

func TestParseSkillMD_MissingName(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	// 有 frontmatter 但缺 name，应退回 UnknownSkillName
	content := "---\ndescription: 只有描述\n---\nbody\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != UnknownSkillName {
		t.Errorf("缺 name 时应退回 UnknownSkillName, 实际 %q", skill.Name)
	}
}
