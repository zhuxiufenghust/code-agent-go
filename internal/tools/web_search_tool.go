// 内置工具：WebSearch（网页搜索工具）。
//
// 使用 DuckDuckGo HTML 端点（html.duckduckgo.com/html/）执行搜索，
// 无需 API Key，零外部平台依赖。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// SearchResult 单条搜索结果
type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

const (
	ddgSearchURL   = "https://html.duckduckgo.com/html/"
	ddgReferrerURL = "https://duckduckgo.com/"
	ddgOriginURL   = "https://duckduckgo.com/"
)

// DDGClient DuckDuckGo HTML 客户端
type WebSearchTool struct {
	httpClient *http.Client
	userAgent  string
	schema.ToolDefinition
}
type webSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// Option 函数式选项
type Option func(*WebSearchTool)

func WithSearchTimeout(d time.Duration) Option {
	return func(c *WebSearchTool) {
		c.httpClient.Timeout = d
	}
}

func WithUserAgent(ua string) Option {
	return func(t *WebSearchTool) {
		t.userAgent = ua
	}
}

// NewDDGClient 创建客户端，带生产默认值
func NewWebSearchTool(opts ...Option) *WebSearchTool {
	c := &WebSearchTool{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			// 跟随重定向，但限制最大跳转次数
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		// 一个常见的桌面浏览器 UA
		userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		ToolDefinition: schema.ToolDefinition{
			Name: "web_search_tool",
			Description: "在互联网上搜索信息，返回标题、URL 和摘要列表。" +
				"使用 DuckDuckGo，无需 API Key。" +
				"搜索后可使用 web_fetch_tool 抓取具体页面内容。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "搜索词，建议使用英文以获得更好效果",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "返回结果数量（默认 5，最大 10）",
					},
				},
				"required": []string{"query"},
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (t *WebSearchTool) GetDefinition() schema.ToolDefinition {
	return t.ToolDefinition
}

func (t *WebSearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params webSearchInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("解析输入参数失败: %v", err)
	}
	if params.Query == "" {
		return "", fmt.Errorf("query is empty")
	}
	if params.MaxResults <= 0 || params.MaxResults > 30 {
		params.MaxResults = 10 // DDG HTML 一页最多约 10-30 条
	}
	results, err := t.Search(ctx, params.Query, "wt-wt", params.MaxResults)
	if err != nil {
		return "", fmt.Errorf("search failed: %v", err)
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("marshal results failed: %v", err)
	}

	return string(resultsJSON), nil
}

// Search 执行搜索
// query: 搜索词
// region: 区域代码，如 "wt-wt"(全球) / "us-en" / "cn-zh"，传空用默认 wt-wt
// maxResults: 最大返回条数
func (t *WebSearchTool) Search(ctx context.Context, query, region string, maxResults int) ([]searchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if maxResults <= 0 || maxResults > 30 {
		maxResults = 10 // DDG HTML 一页最多约 10-30 条
	}
	if region == "" {
		region = "wt-wt"
	}

	// 构建表单数据
	formData := url.Values{}
	formData.Set("q", query)
	formData.Set("kl", region)
	formData.Set("b", "") // 首页 b 为空

	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		ddgSearchURL,
		strings.NewReader(formData.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// 关键：伪装成浏览器
	req.Header.Set("User-Agent", t.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,zh-CN;q=0.8")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", ddgReferrerURL)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo returned HTTP %d", resp.StatusCode)
	}

	// 用 goquery 解析
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	results := make([]searchResult, 0, maxResults)

	doc.Find(".result").Each(func(i int, s *goquery.Selection) {
		if len(results) >= maxResults {
			return
		}
		// 跳过广告
		if s.HasClass("result--ad") {
			return
		}

		// 标题 + 链接
		titleNode := s.Find(".result__title a")
		title := strings.TrimSpace(titleNode.Text())
		href, exists := titleNode.Attr("href")
		if !exists || title == "" {
			return
		}

		// 解码真实 URL（DDG 用 uddg= 包装）
		realURL := decodeDDGURL(href)

		// 摘要
		snippet := strings.TrimSpace(s.Find(".result__snippet").Text())

		results = append(results, searchResult{
			Title:   title,
			URL:     realURL,
			Snippet: snippet,
		})
	})

	return results, nil
}

// decodeDDGURL 从 DuckDuckGo 的重定向包装中提取真实 URL
// DDG 的链接通常是 /l/?uddg=<encoded_url>&rut=<...> 的形式
func decodeDDGURL(href string) string {
	// 情况1：相对路径的 uddg 包装
	if strings.Contains(href, "uddg=") {
		// 补成完整 URL 让 url.Parse 能解析
		fullURL := ddgOriginURL + href
		if strings.HasPrefix(href, "//") {
			fullURL = "https:" + href
		}
		u, err := url.Parse(fullURL)
		if err == nil {
			if actual := u.Query().Get("uddg"); actual != "" {
				// uddg 本身是 URL 编码的，需要解码一次
				if decoded, err := url.QueryUnescape(actual); err == nil {
					return decoded
				}
				return actual
			}
		}
	}

	// 情况2：已经是完整 URL
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	// 情况3：协议相对 URL
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}

	// 情况4：相对路径
	return ddgOriginURL + href
}

// SearchWithRetry 带重试的搜索（生产环境推荐）
func (t *WebSearchTool) SearchWithRetry(ctx context.Context, query, region string, maxResults, maxRetries int) ([]searchResult, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		results, err := t.Search(ctx, query, region, maxResults)
		if err == nil {
			return results, nil
		}
		lastErr = err
		log.Printf("[DDG] attempt %d/%d failed: %v", attempt+1, maxRetries, err)

		// 指数退避
		backoff := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("all retries failed: %w", lastErr)
}
