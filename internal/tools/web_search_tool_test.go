package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mockDDGTransport 是一个 http.RoundTripper，拦截请求并返回预置的
// DuckDuckGo HTML 风格响应，从而让 Search/Execute 在无外网环境下也可测。
type mockDDGTransport struct {
	status      int
	body        string
	callCount   int
	failUntil   int // 前 failUntil 次调用返回错误（用于测试重试）
}

func (m *mockDDGTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.callCount++
	if m.callCount <= m.failUntil {
		return nil, io.ErrUnexpectedEOF
	}
	if m.status == 0 {
		m.status = http.StatusOK
	}
	return &http.Response{
		StatusCode: m.status,
		Status:     http.StatusText(m.status),
		Body:       io.NopCloser(strings.NewReader(m.body)),
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Request:    req,
	}, nil
}

// ddgHTMLFixture 返回一份模拟 DuckDuckGo HTML 结果的页面。
// 包含：1 个广告(应被跳过)、3 个正常结果(其中 2 个带 uddg 包装链接)。
func ddgHTMLFixture() string {
	return `
<html><body>
<div class="result result--ad">
  <h2 class="result__title"><a href="/l/?uddg=https%3A%2F%2Fads.example.com">广告标题</a></h2>
  <a class="result__snippet">这是广告摘要</a>
</div>
<div class="result">
  <h2 class="result__title"><a href="/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1">Example Page 1</a></h2>
  <a class="result__snippet">This is the first snippet.</a>
</div>
<div class="result">
  <h2 class="result__title"><a href="https://example.com/page2">Example Page 2</a></h2>
  <a class="result__snippet">This is the second snippet.</a>
</div>
<div class="result">
  <h2 class="result__title"><a href="/l/?uddg=https%3A%2F%2Fexample.com%2Fpage3">Example Page 3</a></h2>
  <a class="result__snippet">This is the third snippet.</a>
</div>
</body></html>`
}

func newMockSearchTool(t *testing.T, mt *mockDDGTransport) *WebSearchTool {
	t.Helper()
	tool := NewWebSearchTool()
	tool.httpClient = &http.Client{Transport: mt}
	return tool
}

func TestNewWebSearchTool_Definition(t *testing.T) {
	tool := NewWebSearchTool()
	def := tool.GetDefinition()
	if def.Name != "web_search_tool" {
		t.Errorf("期望 tool name 为 web_search_tool, 实际为 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	schemaMap, ok := def.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("期望 input_schema 为 map[string]interface{}, 实际 %T", def.InputSchema)
	}
	required, ok := schemaMap["required"].([]string)
	if !ok || !contains(required, "query") {
		t.Errorf("期望 input_schema.required 包含 query, 实际 %v", schemaMap["required"])
	}
}

func TestWebSearchTool_InvalidJSON(t *testing.T) {
	tool := NewWebSearchTool()
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回错误")
	}
}

func TestWebSearchTool_EmptyQuery(t *testing.T) {
	tool := NewWebSearchTool()
	input, _ := json.Marshal(map[string]interface{}{"query": ""})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("期望 query 为空时返回错误")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestDecodeDDGURL(t *testing.T) {
	cases := []struct {
		name string
		href string
		want string
	}{
		{
			name: "uddg 包装链接",
			href: "/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1",
			want: "https://example.com/page1",
		},
		{
			name: "已经是完整 https URL",
			href: "https://example.com/x",
			want: "https://example.com/x",
		},
		{
			name: "协议相对 URL",
			href: "//cdn.example.com/a",
			want: "https://cdn.example.com/a",
		},
		{
			name: "相对路径",
			href: "/relative/path",
			// ddgOriginURL = "https://duckduckgo.com/" 以 / 结尾，拼接后出现双斜杠
			want: "https://duckduckgo.com//relative/path",
		},
		{
			name: "uddg 双重编码的协议相对里被优先处理",
			href: "//example.com/l/?uddg=https%3A%2F%2Fb.com",
			want: "https://b.com",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeDDGURL(c.href); got != c.want {
				t.Errorf("decodeDDGURL(%q) = %q, 期望 %q", c.href, got, c.want)
			}
		})
	}
}

func TestWebSearchTool_SearchParsesResults(t *testing.T) {
	mt := &mockDDGTransport{body: ddgHTMLFixture()}
	tool := newMockSearchTool(t, mt)

	results, err := tool.Search(context.Background(), "golang", "wt-wt", 10)
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	// 1 个广告被跳过，剩下 3 个正常结果
	if len(results) != 3 {
		t.Fatalf("期望解析出 3 条结果, 实际 %d 条: %+v", len(results), results)
	}
	if results[0].Title != "Example Page 1" {
		t.Errorf("标题不符合预期: %q", results[0].Title)
	}
	if results[0].URL != "https://example.com/page1" {
		t.Errorf("URL 解码不符合预期: %q", results[0].URL)
	}
	if !strings.Contains(results[0].Snippet, "first snippet") {
		t.Errorf("摘要不符合预期: %q", results[0].Snippet)
	}
	// 第二条是完整 URL，无需解码
	if results[1].URL != "https://example.com/page2" {
		t.Errorf("第二条 URL 不符合预期: %q", results[1].URL)
	}
}

func TestWebSearchTool_SkipsAds(t *testing.T) {
	mt := &mockDDGTransport{body: ddgHTMLFixture()}
	tool := newMockSearchTool(t, mt)
	results, err := tool.Search(context.Background(), "golang", "wt-wt", 10)
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	for _, r := range results {
		if strings.Contains(r.Title, "广告") {
			t.Errorf("不应包含广告结果: %+v", r)
		}
		if strings.Contains(r.URL, "ads.example.com") {
			t.Errorf("不应包含广告 URL: %+v", r)
		}
	}
}

func TestWebSearchTool_MaxResultsClamp(t *testing.T) {
	// 页面有 3 条正常结果，限制 maxResults=2 应只返回 2 条
	mt := &mockDDGTransport{body: ddgHTMLFixture()}
	tool := newMockSearchTool(t, mt)
	results, err := tool.Search(context.Background(), "golang", "wt-wt", 2)
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("期望被截断为 2 条, 实际 %d 条", len(results))
	}

	// maxResults 非法(0/超出) 应回退默认 10，返回全部 3 条
	mt2 := &mockDDGTransport{body: ddgHTMLFixture()}
	tool2 := newMockSearchTool(t, mt2)
	results2, err := tool2.Search(context.Background(), "golang", "wt-wt", 0)
	if err != nil {
		t.Fatalf("Search 失败: %v", err)
	}
	if len(results2) != 3 {
		t.Errorf("maxResults=0 应回退默认并返回 3 条, 实际 %d 条", len(results2))
	}
}

func TestWebSearchTool_ExecuteJSONOutput(t *testing.T) {
	mt := &mockDDGTransport{body: ddgHTMLFixture()}
	tool := newMockSearchTool(t, mt)
	input, _ := json.Marshal(map[string]interface{}{
		"query":       "golang",
		"max_results": 2,
	})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	var parsed []searchResult
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("Execute 返回的不是合法 JSON: %v (内容 %q)", err, got)
	}
	if len(parsed) != 2 {
		t.Errorf("期望返回 2 条 JSON 结果, 实际 %d 条", len(parsed))
	}
}

func TestWebSearchTool_HTTPErrorStatus(t *testing.T) {
	mt := &mockDDGTransport{status: http.StatusForbidden, body: "blocked"}
	tool := newMockSearchTool(t, mt)
	_, err := tool.Search(context.Background(), "golang", "wt-wt", 5)
	if err == nil {
		t.Fatal("期望 HTTP 非 200 时返回错误")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("错误信息应包含状态码 403, 实际: %v", err)
	}
}

func TestWebSearchTool_SearchWithRetry(t *testing.T) {
	// 前 2 次调用失败，第 3 次成功（重试 3 次应能拿到结果）
	mt := &mockDDGTransport{body: ddgHTMLFixture(), failUntil: 2}
	tool := newMockSearchTool(t, mt)

	start := time.Now()
	results, err := tool.SearchWithRetry(context.Background(), "golang", "wt-wt", 10, 3)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SearchWithRetry 失败: %v (调用次数 %d)", err, mt.callCount)
	}
	if len(results) != 3 {
		t.Errorf("期望返回 3 条结果, 实际 %d 条", len(results))
	}
	if mt.callCount != 3 {
		t.Errorf("期望重试后共调用 3 次, 实际 %d 次", mt.callCount)
	}
	// 应有退避等待（首次失败退避 500ms）
	if elapsed < 400*time.Millisecond {
		t.Errorf("期望存在退避等待, 实际仅耗时 %v", elapsed)
	}
}

func TestWebSearchTool_SearchWithRetryExhausted(t *testing.T) {
	mt := &mockDDGTransport{status: http.StatusServiceUnavailable, body: "down", failUntil: 99}
	tool := newMockSearchTool(t, mt)
	_, err := tool.SearchWithRetry(context.Background(), "golang", "wt-wt", 10, 2)
	if err == nil {
		t.Fatal("期望全部重试失败后返回错误")
	}
	if !strings.Contains(err.Error(), "all retries failed") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
	if mt.callCount != 2 {
		t.Errorf("期望调用 2 次, 实际 %d 次", mt.callCount)
	}
}

// TestWebSearchTool_LiveNetwork 真实访问 DuckDuckGo，默认跳过，
// 仅当设置环境变量 RUN_LIVE_WEB_SEARCH=1 时执行，用于"实际验证"。
//
// 可通过环境变量覆盖搜索参数：
//   - WEB_SEARCH_QUERY    搜索词（默认 "golang testing"）
//   - WEB_SEARCH_REGION   区域代码（默认 "wt-wt"，全球）
//   - WEB_SEARCH_MAX      返回条数（默认 5）
//   - WEB_SEARCH_LOG      结果输出日志文件路径（默认 "web_search_result.log"）
//   - WEB_SEARCH_PROXY    HTTP 代理地址（如 "http://127.0.0.1:10818"），
//     经代理访问时强制使用 HTTP/1.1（DuckDuckGo 对 HTTP/2 返回 403）
//
// 运行示例：
//
//	RUN_LIVE_WEB_SEARCH=1 WEB_SEARCH_QUERY="go http client" \
//	  WEB_SEARCH_PROXY="http://127.0.0.1:10818" \
//	  go test ./internal/tools/ -run TestWebSearchTool_LiveNetwork -v
func TestWebSearchTool_LiveNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实网络测试")
	}
	if os.Getenv("RUN_LIVE_WEB_SEARCH") != "1" {
		t.Skip("设置 RUN_LIVE_WEB_SEARCH=1 以运行真实网络搜索测试")
	}

	// 从环境变量读取搜索参数，未设置则用默认值
	query := os.Getenv("WEB_SEARCH_QUERY")
	if query == "" {
		query = "golang testing"
	}
	region := os.Getenv("WEB_SEARCH_REGION")
	if region == "" {
		region = "wt-wt"
	}
	maxResults := 5
	if v := os.Getenv("WEB_SEARCH_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxResults = n
		}
	}
	logPath := os.Getenv("WEB_SEARCH_LOG")
	if logPath == "" {
		logPath = "web_search_result.log"
	}

	tool := NewWebSearchTool(WithSearchTimeout(20 * time.Second))
	// 若指定了代理，则让 httpClient 走代理并强制 HTTP/1.1
	if proxy := os.Getenv("WEB_SEARCH_PROXY"); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			t.Fatalf("解析 WEB_SEARCH_PROXY 失败: %v", err)
		}
		tool.httpClient = &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				ForceAttemptHTTP2: false, // 避免 HTTP/2 被 DDG 拒绝(403)
			},
		}
	}
	results, err := tool.SearchWithRetry(context.Background(), query, region, maxResults, 3)
	if err != nil {
		t.Fatalf("真实搜索失败: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("真实搜索未返回任何结果")
	}
	t.Logf("真实搜索获取到 %d 条结果，首条: %s (%s)", len(results), results[0].Title, results[0].URL)

	// 将结果写入日志文件
	var b strings.Builder
	fmt.Fprintf(&b, "搜索词: %s  区域: %s  时间: %s\n", query, region, time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "共 %d 条结果:\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&b, "\n[%d] %s\n    URL: %s\n    摘要: %s\n", i+1, r.Title, r.URL, r.Snippet)
	}
	if err := os.WriteFile(logPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("写入日志文件 %s 失败: %v", logPath, err)
	}
	t.Logf("结果已写入日志文件: %s", logPath)
}
