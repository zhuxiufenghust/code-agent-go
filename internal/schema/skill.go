package schema

// 先只实现简单的skill,
// Assets, Scripts, References 都是相对路径，后续可以扩展为支持远程资源
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// 其他head部分字段先省略，后续需要再完善
	Body string `json:"body"`

	Assets     []Asset     `json:"assets,omitempty"`
	Scripts    []Script    `json:"scripts,omitempty"`
	References []Reference `json:"references,omitempty"`
}

type Asset struct {
	Name string `json:"name"`
	Path string `json:"path"` // 相对于 Skill 的路径，用于组织和引用
}

// ==================== Script 定义 ====================
// Script 代表一个可被 LLM 调用的工具（真正的执行逻辑）
// 注意：Script 不是 Skill 内部直接调用的，而是 LLM 通过 tool_call bash 等工具触发的
type Script struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"` // 脚本文件的路径，相对于 Skill 的目录
}

type Reference struct {
	Name string `json:"name"`
	Path string `json:"path"` // 引用文件的路径，相对于 Skill 的目录
}
