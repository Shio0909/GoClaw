package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// NewWebSearchTool 创建 Tavily 网络搜索工具
func NewWebSearchTool(apiKey string) *ToolDef {
	return &ToolDef{
		Name:        "web_search",
		Description: "使用 Tavily 搜索引擎搜索网络信息，返回相关结果摘要",
		Retryable:   true,
		Parameters: []ParamDef{
			{Name: "query", Type: "string", Description: "搜索关键词", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}

			reqBody := map[string]any{
				"query":              query,
				"search_depth":       "basic",
				"include_answer":     true,
				"max_results":        5,
			}
			bodyBytes, _ := json.Marshal(reqBody)

			req, err := http.NewRequestWithContext(ctx, "POST", "https://api.tavily.com/search", bytes.NewReader(bodyBytes))
			if err != nil {
				return "", err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("tavily request failed: %w", err)
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				return "", fmt.Errorf("tavily API error %d: %s", resp.StatusCode, string(respBody))
			}

			var result tavilyResponse
			if err := json.Unmarshal(respBody, &result); err != nil {
				return "", fmt.Errorf("parse tavily response: %w", err)
			}

			// 格式化输出
			var output string
			if result.Answer != "" {
				output += "📋 摘要: " + result.Answer + "\n\n"
			}
			output += "🔗 搜索结果:\n"
			for i, r := range result.Results {
				output += fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, truncate(r.Content, 200))
			}
			return output, nil
		},
	}
}

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
