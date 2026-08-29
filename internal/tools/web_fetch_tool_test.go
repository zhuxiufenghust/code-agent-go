package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNewWebFetchTool_Definition(t *testing.T) {
	tool := NewWebFetchTool()
	def := tool.Definition()
	if def.Name != "web_fetch" {
		t.Errorf("期望 Definition().Name 为 web_fetch, 实际 %q", def.Name)
	}
	if def.Description == "" {
		t.Error("期望 description 非空")
	}
	schemaMap, ok := def.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("期望 input_schema 为 map[string]interface{}, 实际 %T", def.InputSchema)
	}
	required, ok := schemaMap["required"].([]string)
	if !ok || !contains(required, "url") {
		t.Errorf("期望 input_schema.required 包含 url, 实际 %v", schemaMap["required"])
	}
	// 校验 max_chars 描述的默认值与上限与常量一致
	props, _ := schemaMap["properties"].(map[string]interface{})
	maxField, _ := props["max_chars"].(map[string]interface{})
	desc, _ := maxField["description"].(string)
	if !strings.Contains(desc, strconv.Itoa(defaultMaxChars)) || !strings.Contains(desc, strconv.Itoa(hardMaxChars)) {
		t.Errorf("max_chars 描述应包含默认 %d 与上限 %d, 实际 %q", defaultMaxChars, hardMaxChars, desc)
	}
}

func TestWebFetchTool_InvalidJSON(t *testing.T) {
	tool := NewWebFetchTool()
	_, err := tool.Execute(context.Background(), json.RawMessage("not-json"))
	if err == nil {
		t.Fatal("期望非法 JSON 时返回 error")
	}
	if !strings.Contains(err.Error(), "parse args failed") {
		t.Errorf("错误信息不符合预期: %v", err)
	}
}

func TestWebFetchTool_EmptyURL(t *testing.T) {
	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": ""})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("不应返回 error, 实际 %v", err)
	}
	if !strings.Contains(got, "url parameter is required") {
		t.Errorf("期望提示 url 必填, 实际 %q", got)
	}
}

// stubResolver 将 SSRF 解析依赖替换为返回指定 IP，使 httptest（127.0.0.1）等
// 本地地址在测试中可通过校验；测试结束后自动恢复。
func stubResolver(t *testing.T, ips ...string) {
	t.Helper()
	parsed := make([]net.IP, 0, len(ips))
	for _, s := range ips {
		parsed = append(parsed, net.ParseIP(s))
	}
	orig := resolveHost
	resolveHost = func(string) ([]net.IP, error) { return parsed, nil }
	t.Cleanup(func() { resolveHost = orig })
}

// TestWebFetchTool_HTML 用 httptest 起一个返回 HTML 的服务器，验证 Markdown 转换与来源行。
func TestWebFetchTool_HTML(t *testing.T) {
	stubResolver(t, "203.0.113.1")
	html := `<html><head><title>示例页面</title></head><body>
<h1>标题一</h1>
<p>第一段正文内容。</p>
<p>第二段正文内容，用于提取。</p>
<script>var secret=1;</script>
<style>.x{color:red}</style>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if !strings.Contains(got, "示例页面") {
		t.Errorf("期望包含标题, 实际 %q", got)
	}
	if !strings.Contains(got, "> 来源："+srv.URL) {
		t.Errorf("期望包含来源行, 实际 %q", got)
	}
	// script/style 内容不应进入正文
	if strings.Contains(got, "var secret") || strings.Contains(got, ".x{color:red}") {
		t.Errorf("脚本/样式内容不应被提取: %q", got)
	}
}

func TestWebFetchTool_PlainTextTruncate(t *testing.T) {
	stubResolver(t, "203.0.113.1")
	body := strings.Repeat("A", 40000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// max_chars 超过 hardMaxChars(32000) 应被钳制到 32000 并截断
	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": srv.URL, "max_chars": 999999})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if !strings.Contains(got, "已显示前 32000 字符") {
		t.Errorf("期望按 hardMaxChars(32000) 截断, 实际 %q", got)
	}
}

func TestWebFetchTool_HTTPError(t *testing.T) {
	stubResolver(t, "203.0.113.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("不应返回 error, 实际 %v", err)
	}
	if !strings.Contains(got, "Error: HTTP 404") {
		t.Errorf("期望返回 HTTP 404 错误串, 实际 %q", got)
	}
}

func TestWebFetchTool_UnsupportedContentType(t *testing.T) {
	stubResolver(t, "203.0.113.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("binary"))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("不应返回 error, 实际 %v", err)
	}
	if !strings.Contains(got, "不支持的内容类型") {
		t.Errorf("期望提示不支持的类型, 实际 %q", got)
	}
}

func TestWebFetchTool_MaxCharsClampDefault(t *testing.T) {
	stubResolver(t, "203.0.113.1")
	// 不传 max_chars 时使用 defaultMaxChars(8000)，内容短则不截断
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("short content"))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if strings.Contains(got, "已显示前") {
		t.Errorf("短内容不应被截断, 实际 %q", got)
	}
}

// ---------- SSRF 防护测试 ----------

func TestIsSafeURL_SchemeAndParse(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		stub  []string // 若非空则 stub 解析结果
		isErr bool
	}{
		{name: "ftp 协议被拒", raw: "ftp://example.com", isErr: true},
		{name: "file 协议被拒", raw: "file:///etc/passwd", isErr: true},
		{name: "非法 URL", raw: "://bad", isErr: true},
		{name: "缺少主机名", raw: "http:///path", isErr: true},
		{name: "http 公网 IP 解析", raw: "http://example.com", stub: []string{"93.184.216.34"}, isErr: false},
		{name: "https 公网", raw: "https://example.com", stub: []string{"93.184.216.34"}, isErr: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.stub != nil {
				stubResolver(t, c.stub...)
			}
			err := isSafeURL(c.raw)
			if c.isErr && err == nil {
				t.Errorf("期望 isSafeURL(%q) 返回错误", c.raw)
			}
			if !c.isErr && err != nil {
				t.Errorf("期望 isSafeURL(%q) 通过, 实际 %v", c.raw, err)
			}
		})
	}
}

func TestIsSafeURL_BlocksPrivateIPs(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"环回", "127.0.0.1"},
		{"私有A段", "10.0.0.1"},
		{"私有C段", "192.168.1.1"},
		{"链路本地(云元数据)", "169.254.169.254"},
		{"未指定", "0.0.0.0"},
		{"IPv6环回", "::1"},
		{"IPv6私有", "fd00::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubResolver(t, c.ip)
			if err := isSafeURL("http://internal.example/"); err == nil {
				t.Errorf("期望 %s 被拒绝, 实际通过", c.ip)
			}
		})
	}
}

// TestWebFetchTool_BlocksPrivateIP 验证 Execute 在请求发出前拦截内网地址，
// 且不会真正发起请求。
func TestWebFetchTool_BlocksPrivateIP(t *testing.T) {
	stubResolver(t, "10.0.0.5")
	// 用一个会令测试失败的处理器确认请求未被发出
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	// 用控制解析结果的桩，但 host 仍写成一个普通域名
	input, _ := json.Marshal(map[string]interface{}{"url": "http://blocked.example.com/"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("不应返回 error, 实际 %v", err)
	}
	if !strings.Contains(got, "Error: unsafe or invalid URL") {
		t.Errorf("期望拦截内网地址, 实际 %q", got)
	}
	if hit {
		t.Error("不应向内网地址发起实际请求")
	}
}

// TestWebFetchTool_BlocksRedirectToPrivate 验证重定向到内网时被拦截。
func TestWebFetchTool_BlocksRedirectToPrivate(t *testing.T) {
	// 初始 host(127.0.0.1) 解析为公网，重定向终点 internal.example 解析为内网
	stubResolverForHost(t, map[string][]string{
		"127.0.0.1":       {"203.0.113.1"},
		"internal.example": {"192.168.0.9"},
	})
	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, "http://internal.example/secret", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer redirectSrv.Close()

	tool := NewWebFetchTool()
	input, _ := json.Marshal(map[string]interface{}{"url": redirectSrv.URL + "/go"})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("不应返回 Go error, 实际 %v", err)
	}
	// 重定向校验失败会由 CheckRedirect 返回错误，最终表现为 request failed 字符串
	if !strings.Contains(got, "Error: request failed") {
		t.Fatalf("期望返回 request failed, 实际 %q", got)
	}
	if !strings.Contains(got, "unsafe") && !strings.Contains(got, "redirect") {
		t.Errorf("错误信息应指出重定向不安全, 实际 %q", got)
	}
}

// stubResolverForHost 按主机名返回指定 IP，host 不在映射中时解析失败。
func stubResolverForHost(t *testing.T, m map[string][]string) {
	t.Helper()
	orig := resolveHost
	resolveHost = func(host string) ([]net.IP, error) {
		ips, ok := m[host]
		if !ok {
			return nil, fmt.Errorf("no stub for host %q", host)
		}
		parsed := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			parsed = append(parsed, net.ParseIP(s))
		}
		return parsed, nil
	}
	t.Cleanup(func() { resolveHost = orig })
}

// ---------- web_content.go 单元测试 ----------

func TestExtractContent_HTML(t *testing.T) {
	html := `<html><head><title>文档标题</title></head><body>
<article><p>这是文章正文，应当被提取为 Markdown。</p>
<p>第二段内容，用于验证多段提取。</p></article>
</body></html>`
	out, err := extractContent(strings.NewReader(html), "https://example.com/doc", 8000)
	if err != nil {
		t.Fatalf("extractContent 失败: %v", err)
	}
	if !strings.Contains(out, "文档标题") {
		t.Errorf("期望包含标题, 实际 %q", out)
	}
	if !strings.Contains(out, "这是文章正文") {
		t.Errorf("期望包含正文, 实际 %q", out)
	}
	if !strings.Contains(out, "https://example.com/doc") {
		t.Errorf("期望包含来源 URL, 实际 %q", out)
	}
}

func TestAssemblePage_Truncation(t *testing.T) {
	longContent := strings.Repeat("内容行\n", 2000) // 远超默认上限
	out := assemblePage("https://example.com", "标题", longContent, 100)
	if !strings.Contains(out, "已显示前 100 字符") {
		t.Errorf("期望按 100 字符截断, 实际 %q", out)
	}
	// 截断点应尽量落在换行处，不应出现半句被切断的明显问题（以换行结尾）
	if !strings.Contains(out, "\n\n[内容已截断") {
		t.Errorf("截断标记前应保留段落换行, 实际 %q", out)
	}
}

func TestAssemblePage_RuneBoundary(t *testing.T) {
	// 构造一个以多字节字符(中文)结尾、需要回退到 rune 边界的内容
	content := strings.Repeat("x", 50) + "中文测试"
	out := assemblePage("https://example.com", "", content, 52)
	// 结果必须是合法 UTF-8，且不出现替换字符
	if !strings.Contains(out, "已显示前 52 字符") {
		t.Errorf("期望按 52 字符截断, 实际 %q", out)
	}
	if strings.Contains(out, "�") {
		t.Errorf("截断不应产生无效 UTF-8 替换字符: %q", out)
	}
}

func TestExtractPlainText(t *testing.T) {
	html := `<html><body>
<p>可见文本一。</p>
<script>var hidden=1;</script>
<style>.hidden{display:none}</style>
<p>可见文本二。</p>
</body></html>`
	got := extractPlainText(strings.NewReader(html))
	if !strings.Contains(got, "可见文本一") || !strings.Contains(got, "可见文本二") {
		t.Errorf("期望提取可见文本, 实际 %q", got)
	}
	if strings.Contains(got, "var hidden") || strings.Contains(got, ".hidden") {
		t.Errorf("脚本/样式不应被提取: %q", got)
	}
}

// TestWebFetchTool_LiveNetwork 真实抓取一个页面，默认跳过，
// 需设置 RUN_LIVE_WEB_FETCH=1。可通过 WEB_FETCH_PROXY 指定 HTTP 代理
// （代理场景下强制 HTTP/1.1，避免被目标站点拒绝），结果写入 WEB_FETCH_LOG。
func TestWebFetchTool_LiveNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真实网络测试")
	}
	if os.Getenv("RUN_LIVE_WEB_FETCH") != "1" {
		t.Skip("设置 RUN_LIVE_WEB_FETCH=1 以运行真实抓取测试")
	}

	target := os.Getenv("WEB_FETCH_URL")
	if target == "" {
		target = "https://pkg.go.dev/net/http"
	}
	logPath := os.Getenv("WEB_FETCH_LOG")
	if logPath == "" {
		logPath = "web_fetch_result.log"
	}

	tool := NewWebFetchTool()
	if proxy := os.Getenv("WEB_FETCH_PROXY"); proxy != "" {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			t.Fatalf("解析 WEB_FETCH_PROXY 失败: %v", err)
		}
		tool.client = &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				ForceAttemptHTTP2: false,
			},
		}
	}

	input, _ := json.Marshal(map[string]interface{}{"url": target, "max_chars": 4000})
	got, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("真实抓取失败: %v", err)
	}
	if strings.HasPrefix(got, "Error:") {
		t.Fatalf("抓取返回错误: %s", got)
	}
	t.Logf("真实抓取获取到 %d 字符内容，前 120 字: %s", len(got), truncate(got, 120))

	if err := os.WriteFile(logPath, []byte(got), 0o644); err != nil {
		t.Fatalf("写入日志文件 %s 失败: %v", logPath, err)
	}
	t.Logf("结果已写入日志文件: %s", logPath)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
