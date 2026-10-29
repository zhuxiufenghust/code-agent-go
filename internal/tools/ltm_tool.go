package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhuxiufenghust/code-agent-go/internal/ltm"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// RememberTool 让模型把"以后还用得上的事实"写进长期记忆。
// 它属于写操作：若注册表挂了审批中间件，写入同样会经过人工确认。
type RememberTool struct {
	schema.ToolDefinition
	store ltm.Store
}

type rememberInput struct {
	Content string   `json:"content"`
	Kind    string   `json:"kind,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Source  string   `json:"source,omitempty"`
}

// NewRememberTool 创建长期记忆写入工具。
func NewRememberTool(store ltm.Store) *RememberTool {
	return &RememberTool{
		store: store,
		ToolDefinition: schema.ToolDefinition{
			Name:        "remember_tool",
			Description: "把一段值得长期保留的信息（项目约定、用户偏好、踩过的坑）写入长期记忆，供后续会话检索。content 必须自解释，脱离当前对话也读得懂。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{
						"type":        "string",
						"description": "要记住的内容。写完整句子，不要写代词指代当前对话。",
					},
					"kind": map[string]interface{}{
						"type":        "string",
						"description": "可选分类，如 convention（约定）/ preference（偏好）/ pitfall（踩坑）/ fact（事实）。",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "可选标签，用于提高后续 recall 的命中率，如 [\"构建\", \"docker\"]。",
					},
					"source": map[string]interface{}{
						"type":        "string",
						"description": "可选来源说明（哪个会话/任务产生的这条记忆）。",
					},
				},
				"required": []string{"content"},
			},
		},
	}
}

func (t *RememberTool) GetDefinition() schema.ToolDefinition { return t.ToolDefinition }

func (t *RememberTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params rememberInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("unmarshal remember input failed: %w", err)
	}
	if t.store == nil {
		return "", fmt.Errorf("长期记忆未启用：请在配置里打开 ltm.enabled")
	}
	item := ltm.Item{
		Kind:    params.Kind,
		Content: params.Content,
		Tags:    params.Tags,
		Source:  params.Source,
	}
	if err := t.store.Save(ctx, &item); err != nil {
		return "", err
	}
	return fmt.Sprintf("已记住（id=%d, kind=%s, tags=%v）：%s", item.ID, item.Kind, params.Tags, summarize(params.Content)), nil
}

// RecallTool 让模型按关键词从长期记忆里取回旧知识。
type RecallTool struct {
	schema.ToolDefinition
	store      ltm.Store
	maxResults int
}

type recallInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// NewRecallTool 创建长期记忆检索工具。maxResults <= 0 时取 defaultLtmMaxResults。
func NewRecallTool(store ltm.Store, maxResults int) *RecallTool {
	if maxResults <= 0 {
		maxResults = defaultLtmMaxResults
	}
	return &RecallTool{
		store:      store,
		maxResults: maxResults,
		ToolDefinition: schema.ToolDefinition{
			Name:        "recall_tool",
			Description: "按关键词检索长期记忆，取回之前会话记住的项目约定/偏好/踩坑结论。需要回忆旧信息或判断过去是否有过相关决策时使用。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "检索关键词，多个词用空格分隔（命中词多者排前）。",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "可选返回条数上限。",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// defaultLtmMaxResults 是未配置时的单次检索返回上限。
const defaultLtmMaxResults = 5

func (t *RecallTool) GetDefinition() schema.ToolDefinition { return t.ToolDefinition }

func (t *RecallTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params recallInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("unmarshal recall input failed: %w", err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return "", fmt.Errorf("recall 的 query 不能为空")
	}
	if t.store == nil {
		return "", fmt.Errorf("长期记忆未启用：请在配置里打开 ltm.enabled")
	}
	limit := params.Limit
	if limit <= 0 {
		limit = t.maxResults
	}
	items, err := t.store.Search(ctx, params.Query, limit)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "长期记忆中没有命中任何条目。", nil
	}
	// 固定输出格式：模型需要能稳定地把 id 拿出来做删除/引用，
	// 因此这里用"序号 + id"的行式结构而不是自由文本。
	var b strings.Builder
	fmt.Fprintf(&b, "长期记忆命中 %d 条：\n", len(items))
	for i, it := range items {
		tags := ""
		if len(it.Tags) > 0 {
			tags = " tags=" + strings.Join(it.Tags, "|")
		}
		fmt.Fprintf(&b, "%d. [id=%d kind=%s%s %s] %s\n", i+1, it.ID, it.Kind, tags,
			it.CreatedAt.Format("2006-01-02 15:04"), it.Content)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// summarize 把记忆内容压缩成一句话回显，避免把长文本原样重复一遍浪费上下文。
func summarize(content string) string {
	const max = 80
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) <= max {
		return content
	}
	return string(runes[:max]) + "..."
}
