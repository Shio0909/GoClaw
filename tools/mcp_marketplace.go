package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MarketplaceResult Smithery 搜索结果条目
type MarketplaceResult struct {
	QualifiedName string `json:"qualifiedName"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	Verified      bool   `json:"verified"`
	UseCount      int    `json:"useCount"`
	Homepage      string `json:"homepage"`
}

type smitheryResponse struct {
	Servers    []MarketplaceResult `json:"servers"`
	Pagination struct {
		TotalCount int `json:"totalCount"`
	} `json:"pagination"`
}

// NewMCPMarketplaceSearchTool 从 Smithery 在线市场搜索 MCP Server
func NewMCPMarketplaceSearchTool(smitheryKey string) *ToolDef {
	return &ToolDef{
		Name:        "mcp_marketplace_search",
		Description: "从 Smithery 在线市场搜索 MCP Server。精选列表没有结果时使用此工具搜索更多选择",
		Parameters: []ParamDef{
			{Name: "query", Type: "string", Description: "搜索关键词", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query 为必填")
			}

			// 5 秒超时
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			apiURL := fmt.Sprintf("https://registry.smithery.ai/servers?q=%s&pageSize=10", url.QueryEscape(query))
			req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
			if err != nil {
				return "", err
			}
			req.Header.Set("Authorization", "Bearer "+smitheryKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "Smithery 市场暂时不可用，建议用 web_search 搜索 MCP Server。", nil
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				return fmt.Sprintf("Smithery 返回 %d，建议用 web_search 搜索。", resp.StatusCode), nil
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024))
			if err != nil {
				return "读取 Smithery 响应失败，建议用 web_search 搜索。", nil
			}

			var result smitheryResponse
			if err := json.Unmarshal(body, &result); err != nil {
				return "解析 Smithery 响应失败，建议用 web_search 搜索。", nil
			}

			if len(result.Servers) == 0 {
				return fmt.Sprintf("Smithery 市场未找到匹配 \"%s\" 的 MCP Server。可以用 web_search 继续搜索。", query), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Smithery 市场找到 %d 个结果（共 %d 个）：\n\n", len(result.Servers), result.Pagination.TotalCount))
			for _, s := range result.Servers {
				verified := ""
				if s.Verified {
					verified = " [已认证]"
				}
				sb.WriteString(fmt.Sprintf("• %s%s — %s\n", s.QualifiedName, verified, s.Description))
				if s.UseCount > 0 {
					sb.WriteString(fmt.Sprintf("  使用量: %d\n", s.UseCount))
				}
			}
			sb.WriteString("\n注意：市场搜索到的包需要手动指定 command/args 安装，非官方包会触发安全审查。")
			return sb.String(), nil
		},
	}
}
