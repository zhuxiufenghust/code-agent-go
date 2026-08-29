// HTML 内容提取与 Markdown 转换工具。
//
// 管线：go-readability（提取主内容）→ html-to-markdown（转换格式）→ 截断保护。
// fallback：readability 失败时，用 golang.org/x/net/html tokenizer 提取纯文本。
package tools

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

const (
	defaultMaxChars = 8_000
	hardMaxChars    = 32_000
	maxHTMLBodySize = 1 << 20 // 1MB
)

// extractContent 从 HTML 流中提取主内容，返回 Markdown 格式字符串，截断至 maxChars。
func extractContent(body io.Reader, rawURL string, maxChars int) (string, error) {
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	if maxChars > hardMaxChars {
		maxChars = hardMaxChars
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(body, int64(maxHTMLBodySize)+1))
	if err != nil {
		return "", fmt.Errorf("read body failed: %w", err)
	}
	if len(bodyBytes) > maxHTMLBodySize {
		return "", fmt.Errorf("page exceeds 1MB size limit")
	}

	parsedURL, parseErr := url.Parse(rawURL)
	if parseErr != nil || parsedURL == nil {
		parsedURL = &url.URL{}
	}

	var title, content string
	article, readErr := readability.FromReader(bytes.NewReader(bodyBytes), parsedURL)
	if readErr == nil && article.Content != "" {
		title = article.Title
		converter := md.NewConverter("", true, nil)
		if mdContent, convErr := converter.ConvertString(article.Content); convErr == nil {
			content = mdContent
		} else {
			content = article.TextContent
		}
	} else {
		content = extractPlainText(bytes.NewReader(bodyBytes))
	}

	return assemblePage(rawURL, title, content, maxChars), nil
}

// assemblePage 组装最终输出字符串，超出 maxChars 时追加截断标记。
// 截断点优先对齐到最后一个换行符（保证段落完整），并在 UTF-8 rune 边界处截断，
// 避免切断多字节字符（如中文、emoji）产生无效 UTF-8。
func assemblePage(rawURL, title, content string, maxChars int) string {
	var sb strings.Builder
	if title != "" {
		sb.WriteString("# ")
		sb.WriteString(title)
		sb.WriteString("\n\n")
	}
	sb.WriteString("> 来源：")
	sb.WriteString(rawURL)
	sb.WriteString("\n\n")
	sb.WriteString(content)

	result := sb.String()
	if len(result) <= maxChars {
		return result
	}

	// 优先在 maxChars 前的最后一个换行处截断（保证段落完整），
	// 若无合适换行位置则直接在 maxChars 处截断。
	cut := maxChars
	if idx := strings.LastIndex(result[:maxChars], "\n"); idx > maxChars/2 {
		cut = idx
	}
	// 从 cut 向前回退到 UTF-8 rune 边界，防止截断多字节字符产生无效 UTF-8。
	for cut > 0 && result[cut]&0xC0 == 0x80 {
		cut--
	}
	return result[:cut] + fmt.Sprintf("\n\n[内容已截断，已显示前 %d 字符]", maxChars)
}

// extractPlainText 是 go-readability 失败时的兜底实现。
// 使用 golang.org/x/net/html tokenizer 跳过 script/style，提取可见文本。
func extractPlainText(r io.Reader) string {
	tokenizer := html.NewTokenizer(r)
	var sb strings.Builder
	skipDepth := 0

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return strings.TrimSpace(sb.String())
		case html.StartTagToken:
			tag, _ := tokenizer.TagName()
			if string(tag) == "script" || string(tag) == "style" {
				skipDepth++
			}
		case html.EndTagToken:
			tag, _ := tokenizer.TagName()
			if string(tag) == "script" || string(tag) == "style" {
				if skipDepth > 0 {
					skipDepth--
				}
			}
		case html.TextToken:
			if skipDepth == 0 {
				text := strings.TrimSpace(string(tokenizer.Text()))
				if text != "" {
					sb.WriteString(text)
					sb.WriteByte('\n')
				}
			}
		}
	}
}
