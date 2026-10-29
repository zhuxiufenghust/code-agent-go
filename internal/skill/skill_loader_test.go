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
	// 技能目录不存在应为非致命：仅告警并跳过，不返回 error，技能表为空
	loader := NewSkillLoader(filepath.Join(t.TempDir(), "no_such"), t.TempDir())
	if err := loader.LoadSkills(); err != nil {
		t.Fatalf("缺失技能目录不应返回错误, 实际: %v", err)
	}
	if got := loader.GetSkillMap(); len(got) != 0 {
		t.Errorf("缺失技能目录时技能表应为空, 实际 %d 个", len(got))
	}
}

func TestSkillLoader_LoadOneSkill_Valid(t *testing.T) {
	base := t.TempDir()
	writeSkillDir(t, base, "foo", "做foo")
	loader := NewSkillLoader(base, t.TempDir())

	skill, err := loader.LoadOneSkill(filepath.Join(base, ".config/code-agent-go/skills", "foo"))
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

// TestSkillLoader_LoadOneSkill_Resources 验证 references/ scripts/ assets/ 下的
// 文件被扫进技能对象：这些素材由模型在阅读正文后按需取用，不必预先注入上下文。
func TestSkillLoader_LoadOneSkill_Resources(t *testing.T) {
	base := t.TempDir()
	writeSkillDir(t, base, "foo", "做foo")
	skillDir := filepath.Join(base, ".config/code-agent-go/skills", "foo")
	for _, rel := range []string{"references/api.md", "references/nested/more.md", "scripts/run.sh", "assets/tpl.txt"} {
		writeSkillResource(t, base, "foo", rel)
	}

	loader := NewSkillLoader(base, t.TempDir())
	skill, err := loader.LoadOneSkill(skillDir)
	if err != nil {
		t.Fatalf("加载技能失败: %v", err)
	}

	// 路径相对技能目录，供 read/bash 直接引用。
	refPaths := make([]string, 0, len(skill.References))
	for _, r := range skill.References {
		refPaths = append(refPaths, r.Path)
	}
	assertPaths(t, "references", refPaths, []string{"references/api.md", "references/nested/more.md"})

	scriptPaths := make([]string, 0, len(skill.Scripts))
	for _, s := range skill.Scripts {
		scriptPaths = append(scriptPaths, s.Path)
	}
	assertPaths(t, "scripts", scriptPaths, []string{"scripts/run.sh"})

	assetPaths := make([]string, 0, len(skill.Assets))
	for _, a := range skill.Assets {
		assetPaths = append(assetPaths, a.Path)
	}
	assertPaths(t, "assets", assetPaths, []string{"assets/tpl.txt"})

	// Name 相对资源目录（给 LLM 阅读），Path 相对技能目录（给工具引用）。
	if skill.References[0].Name != "api.md" {
		t.Errorf("期望 references[0].Name=api.md, 实际 %q", skill.References[0].Name)
	}
}

// assertPaths 校验资源路径集合与顺序（WalkDir 保证字典序，顺序稳定才可断言）。
func assertPaths(t *testing.T, kind string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("期望 %d 个 %s, 实际 %d 个: %v", len(want), kind, len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s 第 %d 项不符: 期望 %q, 实际 %q", kind, i, want[i], got[i])
		}
	}
}

// TestSkillLoader_LoadOneSkill_NoResources 验证技能不带资源时字段为空、不报错。
func TestSkillLoader_LoadOneSkill_NoResources(t *testing.T) {
	base := t.TempDir()
	writeSkillDir(t, base, "foo", "做foo")

	loader := NewSkillLoader(base, t.TempDir())
	skill, err := loader.LoadOneSkill(filepath.Join(base, ".config/code-agent-go/skills", "foo"))
	if err != nil {
		t.Fatalf("加载技能失败: %v", err)
	}
	if len(skill.References) != 0 || len(skill.Scripts) != 0 || len(skill.Assets) != 0 {
		t.Errorf("无资源目录时三个资源列表应为空, 实际: %+v", skill)
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

// TestParseSkillMD_QuotedFrontmatter 验证 YAML 带引号的标量会被剥掉外层引号。
// frontmatter 里为了让值包含引号/冒号，常写成 description: "..."，
// 若不剥引号，这些引号会一路出现在工具描述里。
func TestParseSkillMD_QuotedFrontmatter(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "---\nname: \"alpha\"\ndescription: \"含'引号'的描述: 前缀\"\n---\nbody\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != "alpha" {
		t.Errorf("双引号应被剥离, Name 实际 %q", skill.Name)
	}
	if skill.Description != "含'引号'的描述: 前缀" {
		t.Errorf("双引号应被剥离, Description 实际 %q", skill.Description)
	}
}

// TestParseSkillMD_SingleQuoted 单引号同样需要剥离。
func TestParseSkillMD_SingleQuoted(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "---\nname: 'beta'\ndescription: '含\"双引号\"的描述'\n---\nbody\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != "beta" {
		t.Errorf("单引号应被剥离, Name 实际 %q", skill.Name)
	}
	if skill.Description != `含"双引号"的描述` {
		t.Errorf("单引号应被剥离, Description 实际 %q", skill.Description)
	}
}

// TestParseSkillMD_MismatchedQuoteLeftAsIs 引号不成对时不改动原值。
func TestParseSkillMD_MismatchedQuoteLeftAsIs(t *testing.T) {
	loader := NewSkillLoader(t.TempDir(), t.TempDir())
	content := "---\nname: al\"pha\ndescription: 前缀\" suffix\n---\nbody\n"
	skill := loader.parseSkillMD(content)
	if skill.Name != `al"pha` {
		t.Errorf("不成对引号不应被改动, 实际 %q", skill.Name)
	}
	if skill.Description != `前缀" suffix` {
		t.Errorf("不成对引号不应被改动, 实际 %q", skill.Description)
	}
}

// TestSkillLoader_LoadOneSkill_BaseDir 验证技能的绝对目录被记录：
// 资源 Path 是相对它的，没有它模型拿到的相对路径无法解析。
func TestSkillLoader_LoadOneSkill_BaseDir(t *testing.T) {
	base := t.TempDir()
	writeSkillDir(t, base, "foo", "做foo")

	loader := NewSkillLoader(base, t.TempDir())
	skill, err := loader.LoadOneSkill(filepath.Join(base, ".config/code-agent-go/skills", "foo"))
	if err != nil {
		t.Fatalf("加载技能失败: %v", err)
	}
	want := filepath.Join(base, ".config/code-agent-go/skills", "foo")
	if skill.BaseDir != want {
		t.Errorf("期望 BaseDir=%q, 实际 %q", want, skill.BaseDir)
	}
	if !filepath.IsAbs(skill.BaseDir) {
		t.Errorf("BaseDir 应为绝对路径, 实际 %q", skill.BaseDir)
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
