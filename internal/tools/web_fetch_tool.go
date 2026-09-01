// 内置工具：WebFetch（网页抓取工具）。
//
// 抓取指定 URL 的网页，提取主内容并返回 Markdown 格式。
// 所有请求在发出前通过 isSafeURL 校验，防止 SSRF 攻击。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhuxiufenghust/code-agent-go/internal/schema"
)

// resolveHost 是 SSRF 防护的依赖点（便于测试时替换为可控解析器）。
// 默认使用系统 DNS 解析。
var resolveHost = func(host string) ([]net.IP, error) {
	return net.LookupIP(host)
}

const (
	fetchTimeout      = 15 * time.Second
	fetchMaxRedirects = 5
	webUserAgent      = "harness9/1.0"
)

// WebFetchTool 实现 BaseTool 接口，抓取网页并返回 Markdown 内容。
type WebFetchTool struct {
	schema.ToolDefinition
	client *http.Client
}
type WebFetchToolInput struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

// NewWebFetchTool 创建生产用的 WebFetchTool（含完整 SSRF 检查）。
func NewWebFetchTool() *WebFetchTool {
	t := &WebFetchTool{
		ToolDefinition: schema.ToolDefinition{
			Name: "web_fetch_tool",
			Description: "抓取指定 URL 的网页内容，返回 Markdown 格式的主要内容。" +
				"适合读取文档、博客、新闻等页面。" +
				"若需先搜索再抓取，请先使用 web_search 获取 URL。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "要抓取的完整 URL，必须以 http:// 或 https:// 开头",
					},
					"max_chars": map[string]interface{}{
						"type": "integer",
						"description": fmt.Sprintf(
							"返回内容的最大字符数（默认 %d，最大 %d）",
							defaultMaxChars, hardMaxChars,
						),
					},
					"required": []string{"url"},
				},
			},
		},
	}
	t.client = &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirects {
				return fmt.Errorf("exceeded max redirects (%d)", fetchMaxRedirects)
			}
			// 重定向目标同样需通过 SSRF 校验，防止借重定向打内网
			if err := isSafeURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect to unsafe URL: %w", err)
			}
			return nil
		},
	}
	return t
}

func (t *WebFetchTool) Name() string { return "web_fetch" }

func (t *WebFetchTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "抓取指定 URL 的网页内容，返回 Markdown 格式的主要内容。" +
			"适合读取文档、博客、新闻等页面。" +
			"若需先搜索再抓取，请先使用 web_search 获取 URL。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "要抓取的完整 URL，必须以 http:// 或 https:// 开头",
				},
				"max_chars": map[string]interface{}{
					"type": "integer",
					"description": fmt.Sprintf(
						"返回内容的最大字符数（默认 %d，最大 %d）",
						defaultMaxChars, hardMaxChars,
					),
				},
			},
			"required": []string{"url"},
		},
	}
}

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

func (t *WebFetchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input webFetchArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("parse args failed: %w", err)
	}
	if input.URL == "" {
		return "Error: url parameter is required", nil
	}

	// SSRF 防护：发出请求前校验 URL 与解析出的 IP 均非内网/保留地址
	if err := isSafeURL(input.URL); err != nil {
		return fmt.Sprintf("Error: unsafe or invalid URL — %v", err), nil
	}

	maxChars := input.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	if maxChars > hardMaxChars {
		maxChars = hardMaxChars
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.URL, nil)
	if err != nil {
		return fmt.Sprintf("Error: create request failed — %v", err), nil
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept", "text/html,text/plain,*/*")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Sprintf("Error: request failed — %v", err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("Error: HTTP %d %s", resp.StatusCode, resp.Status), nil
	}

	contentType := resp.Header.Get("Content-Type")
	switch {
	case strings.Contains(contentType, "text/html"):
		return extractContent(resp.Body, input.URL, maxChars)
	case strings.HasPrefix(contentType, "text/"):
		data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxChars)+1))
		if err != nil {
			return fmt.Sprintf("Error: read body failed — %v", err), nil
		}
		text := string(data)
		if len(text) > maxChars {
			// 回退到 UTF-8 rune 边界，防止截断多字节字符
			cut := maxChars
			for cut > 0 && text[cut]&0xC0 == 0x80 {
				cut--
			}
			return text[:cut] + fmt.Sprintf("\n\n[内容已截断，已显示前 %d 字符]", maxChars), nil
		}
		return text, nil
	default:
		return fmt.Sprintf("不支持的内容类型：%s", contentType), nil
	}
}

// isSafeURL 校验 URL 是否可安全抓取，防止 SSRF 攻击：
//  1. 仅允许 http / https 协议；
//  2. 必须包含非空主机名；
//  3. 解析主机得到的所有 IP 必须全部为公网地址（拒绝环回、私有、链路本地、
//     未指定、组播及保留地址）。
//
// 通过 resolveHost 进行 DNS 解析，便于在测试中替换为可控实现。
func isSafeURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("解析 URL 失败: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("不支持的协议 %q，仅允许 http/https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}

	ips, err := resolveHost(host)
	if err != nil {
		return fmt.Errorf("解析主机 %q 失败: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("主机 %q 未解析到任何 IP", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("主机 %q 解析到非公网地址 %v", host, ip)
		}
	}
	return nil
}

// isPublicIP 判断 IP 是否为公网地址。环回、私有、链路本地、未指定、
// 组播、保留地址均视为不安全。
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	return true
}
