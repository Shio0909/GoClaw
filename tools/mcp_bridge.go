package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServerConfig 一个 MCP Server 的配置
type MCPServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPBridge 管理多个 MCP Server 连接，将远程工具桥接到本地 Registry
type MCPBridge struct {
	sessions map[string]*mcp.ClientSession
	toolMap  map[string]*mcp.ClientSession // toolName -> session
}

// NewMCPBridge 创建 MCP 桥接器
func NewMCPBridge() *MCPBridge {
	return &MCPBridge{
		sessions: make(map[string]*mcp.ClientSession),
		toolMap:  make(map[string]*mcp.ClientSession),
	}
}

// Connect 连接到一个 MCP Server 并注册其工具
func (b *MCPBridge) Connect(ctx context.Context, cfg MCPServerConfig, registry *Registry) error {
	// 构建命令
	cmd := exec.Command(cfg.Command, cfg.Args...)

	// 设置环境变量
	if len(cfg.Env) > 0 {
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	// 创建 MCP Client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "goclaw",
		Version: "1.0.0",
	}, nil)

	// 通过 stdio 连接
	transport := mcp.NewCommandTransport(cmd)
	session, err := client.Connect(ctx, transport)
	if err != nil {
		return fmt.Errorf("connect to MCP server %s: %w", cfg.Name, err)
	}

	b.sessions[cfg.Name] = session

	// 列出远程工具并注册到本地
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools from %s: %w", cfg.Name, err)
	}

	for _, tool := range toolsResult.Tools {
		b.toolMap[tool.Name] = session
		registry.Register(b.wrapMCPTool(cfg.Name, tool, session))
	}

	fmt.Printf("  已连接 MCP Server: %s (%d 个工具)\n", cfg.Name, len(toolsResult.Tools))
	return nil
}

// wrapMCPTool 将 MCP 远程工具包装为本地 ToolDef
func (b *MCPBridge) wrapMCPTool(serverName string, tool *mcp.Tool, session *mcp.ClientSession) *ToolDef {
	// 从 JSON Schema 提取参数定义
	var params []ParamDef
	if tool.InputSchema != nil && tool.InputSchema.Properties != nil {
		required := make(map[string]bool)
		for _, r := range tool.InputSchema.Required {
			required[r] = true
		}
		for name, prop := range tool.InputSchema.Properties {
			p := ParamDef{
				Name:     name,
				Required: required[name],
			}
			if prop.Type != "" {
				p.Type = string(prop.Type)
			} else {
				p.Type = "string"
			}
			if prop.Description != "" {
				p.Description = prop.Description
			}
			params = append(params, p)
		}
	}

	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("[MCP:%s] %s", serverName, tool.Name)
	} else {
		desc = fmt.Sprintf("[MCP:%s] %s", serverName, desc)
	}

	return &ToolDef{
		Name:        tool.Name,
		Description: desc,
		Parameters:  params,
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      tool.Name,
				Arguments: args,
			})
			if err != nil {
				return "", fmt.Errorf("MCP call %s/%s: %w", serverName, tool.Name, err)
			}

			// 提取文本内容
			var parts []string
			for _, content := range result.Content {
				if tc, ok := content.(*mcp.TextContent); ok {
					parts = append(parts, tc.Text)
				} else {
					// 其他类型序列化为 JSON
					data, _ := json.Marshal(content)
					parts = append(parts, string(data))
				}
			}
			return strings.Join(parts, "\n"), nil
		},
	}
}

// Close 关闭所有 MCP 连接
func (b *MCPBridge) Close() {
	for name, session := range b.sessions {
		_ = session.Close()
		delete(b.sessions, name)
	}
}
