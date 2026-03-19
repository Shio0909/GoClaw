package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// NewWebFetchTool 对应 OpenClaw 的 web_fetch - 抓取网页内容
func NewWebFetchTool() *ToolDef {
	return &ToolDef{
		Name:        "web_fetch",
		Description: "抓取指定 URL 的网页内容，返回纯文本（自动去除 HTML 标签）",
		Parameters: []ParamDef{
			{Name: "url", Type: "string", Description: "要抓取的网页 URL", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			rawURL, _ := args["url"].(string)
			if rawURL == "" {
				return "", fmt.Errorf("url is required")
			}
			if _, err := url.ParseRequestURI(rawURL); err != nil {
				return "", fmt.Errorf("invalid url: %w", err)
			}

			req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
			if err != nil {
				return "", err
			}
			req.Header.Set("User-Agent", "GoClaw/1.0")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("fetch failed: %w", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024)) // 限制 100KB
			if err != nil {
				return "", err
			}

			// 简单提取文本
			text := extractText(body)
			// 截断过长内容
			runes := []rune(text)
			if len(runes) > 3000 {
				text = string(runes[:3000]) + "\n...(内容已截断)"
			}

			return fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", rawURL, resp.StatusCode, text), nil
		},
	}
}

// extractText 从 HTML 中提取纯文本
func extractText(htmlBytes []byte) string {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return string(htmlBytes)
	}

	var sb strings.Builder
	var extract func(*html.Node)
	extract = func(n *html.Node) {
		// 跳过 script 和 style
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				sb.WriteString(text)
				sb.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
		// 块级元素后加换行
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "br", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr":
				sb.WriteString("\n")
			}
		}
	}
	extract(doc)
	return strings.TrimSpace(sb.String())
}

// NewHTTPRequestTool 简单的 HTTP 请求工具（对接 API）
func NewHTTPRequestTool() *ToolDef {
	return &ToolDef{
		Name:        "http_request",
		Description: "发送 HTTP 请求（GET/POST），用于调用外部 API",
		Parameters: []ParamDef{
			{Name: "method", Type: "string", Description: "HTTP 方法: GET 或 POST", Required: true},
			{Name: "url", Type: "string", Description: "请求 URL", Required: true},
			{Name: "body", Type: "string", Description: "POST 请求体 (JSON 格式)", Required: false},
			{Name: "headers", Type: "string", Description: "自定义请求头 (JSON 格式, 如 {\"Authorization\":\"Bearer xxx\"})", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			method, _ := args["method"].(string)
			rawURL, _ := args["url"].(string)
			bodyStr, _ := args["body"].(string)
			headersStr, _ := args["headers"].(string)

			method = strings.ToUpper(method)
			if method != "GET" && method != "POST" {
				return "", fmt.Errorf("only GET and POST are supported")
			}

			var bodyReader io.Reader
			if bodyStr != "" {
				bodyReader = strings.NewReader(bodyStr)
			}

			req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
			if err != nil {
				return "", err
			}
			req.Header.Set("User-Agent", "GoClaw/1.0")
			if bodyStr != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			// 解析自定义 headers
			if headersStr != "" {
				var headers map[string]string
				if err := json.Unmarshal([]byte(headersStr), &headers); err == nil {
					for k, v := range headers {
						req.Header.Set(k, v)
					}
				}
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 50*1024))
			return fmt.Sprintf("Status: %d\n\n%s", resp.StatusCode, string(respBody)), nil
		},
	}
}
