package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authTransport 为 HTTP 请求注入 Authorization 头
type authTransport struct {
	base   http.RoundTripper
	apiKey string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	return t.base.RoundTrip(req)
}

// MCPServerConfig 一个 MCP Server 的配置
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Command   string            `json:"command,omitempty"`   // stdio 模式
	Args      []string          `json:"args,omitempty"`      // stdio 模式
	Env       map[string]string `json:"env,omitempty"`
	Endpoint  string            `json:"endpoint,omitempty"`  // HTTP 模式（SSE 或 Streamable HTTP）
	Transport string            `json:"transport,omitempty"` // "stdio"(默认), "sse", "streamable_http"
	APIKey    string            `json:"api_key,omitempty"`   // HTTP 模式的认证 key
	ToolNames []string          `json:"tool_names,omitempty"` // 只导入指定工具，空则全部导入
}

// MCPBridge 管理多个 MCP Server 连接，将远程工具桥接到本地 Registry
type MCPBridge struct {
	mu       sync.Mutex
	sessions map[string]*mcp.ClientSession
	configs  map[string]MCPServerConfig    // 保存配置用于重连
	toolMap  map[string]string             // toolName -> serverName（动态查 session）
	// serverTools 记录每个 server 注册了哪些工具名，用于卸载
	serverTools map[string][]string // serverName -> []toolName
}

const (
	mcpMaxRetries    = 3             // MCP 工具调用最大重试次数
	mcpRetryBaseWait = 2 * time.Second // 重试基础等待时间
)

// NewMCPBridge 创建 MCP 桥接器
func NewMCPBridge() *MCPBridge {
	return &MCPBridge{
		sessions:    make(map[string]*mcp.ClientSession),
		configs:     make(map[string]MCPServerConfig),
		toolMap:     make(map[string]string),
		serverTools: make(map[string][]string),
	}
}

// newHTTPClient 创建带认证的 HTTP 客户端（如果需要）
func newHTTPClient(apiKey string) *http.Client {
	if apiKey == "" {
		return nil
	}
	return &http.Client{
		Transport: &authTransport{base: http.DefaultTransport, apiKey: apiKey},
	}
}

// Connect 连接到一个 MCP Server 并注册其工具
// 自动根据配置选择 stdio/sse/streamable_http 传输层
func (b *MCPBridge) Connect(ctx context.Context, cfg MCPServerConfig, registry *Registry) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	session, err := b.dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect to MCP server %s: %w", cfg.Name, err)
	}

	b.sessions[cfg.Name] = session
	b.configs[cfg.Name] = cfg

	// 列出远程工具并注册到本地
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools from %s: %w", cfg.Name, err)
	}

	// 构建工具名过滤集
	allowedTools := make(map[string]bool)
	for _, tn := range cfg.ToolNames {
		allowedTools[tn] = true
	}

	var toolNames []string
	for _, tool := range toolsResult.Tools {
		if len(allowedTools) > 0 && !allowedTools[tool.Name] {
			continue
		}
		b.toolMap[tool.Name] = cfg.Name
		registry.Register(b.wrapMCPTool(cfg.Name, *tool))
		toolNames = append(toolNames, tool.Name)
	}
	b.serverTools[cfg.Name] = toolNames

	label := strings.ToLower(cfg.Transport)
	if label == "" {
		label = "stdio"
	}
	fmt.Printf("  已连接 MCP Server: %s [%s] (%d 个工具)\n", cfg.Name, label, len(toolNames))
	return nil
}

// dial 根据配置创建 MCP 连接（不持锁，由调用者持锁）
func (b *MCPBridge) dial(ctx context.Context, cfg MCPServerConfig) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "goclaw",
		Version: "1.0.0",
	}, nil)

	var transport mcp.Transport
	transportType := strings.ToLower(cfg.Transport)

	switch transportType {
	case "sse":
		transport = &mcp.SSEClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: newHTTPClient(cfg.APIKey),
		}
	case "streamable_http":
		transport = &mcp.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: newHTTPClient(cfg.APIKey),
		}
	default: // "stdio" 或空
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			for k, v := range cfg.Env {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
			}
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}

	return client.Connect(ctx, transport, nil)
}

// wrapMCPTool 将 MCP 远程工具包装为本地 ToolDef
// 闭包不直接捕获 session，而是通过 bridge 动态查找，支持重连后自动生效
func (b *MCPBridge) wrapMCPTool(serverName string, tool mcp.Tool) *ToolDef {
	// 从 InputSchema (any → map[string]any) 提取参数定义
	var params []ParamDef
	if schemaMap, ok := tool.InputSchema.(map[string]any); ok {
		required := make(map[string]bool)
		if reqList, ok := schemaMap["required"].([]any); ok {
			for _, r := range reqList {
				if s, ok := r.(string); ok {
					required[s] = true
				}
			}
		}
		if props, ok := schemaMap["properties"].(map[string]any); ok {
			for name, propRaw := range props {
				p := ParamDef{
					Name:     name,
					Required: required[name],
					Type:     "string",
				}
				if propMap, ok := propRaw.(map[string]any); ok {
					if t, ok := propMap["type"].(string); ok {
						p.Type = t
					}
					if d, ok := propMap["description"].(string); ok {
						p.Description = d
					}
				}
				params = append(params, p)
			}
		}
	}

	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("[MCP:%s] %s", serverName, tool.Name)
	} else {
		desc = fmt.Sprintf("[MCP:%s] %s", serverName, desc)
	}

	toolName := tool.Name
	return &ToolDef{
		Name:        toolName,
		Description: desc,
		Parameters:  params,
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			return b.callWithRetry(ctx, serverName, toolName, args)
		},
	}
}

// callWithRetry 带重试和自动重连的 MCP 工具调用
func (b *MCPBridge) callWithRetry(ctx context.Context, serverName, toolName string, args map[string]any) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= mcpMaxRetries; attempt++ {
		session := b.getSession(serverName)
		if session == nil {
			log.Printf("[MCP] %s 无活跃连接，尝试重连 (attempt %d/%d)", serverName, attempt, mcpMaxRetries)
			if err := b.reconnect(ctx, serverName); err != nil {
				lastErr = fmt.Errorf("MCP %s 重连失败: %w", serverName, err)
				b.retryWait(ctx, attempt)
				continue
			}
			session = b.getSession(serverName)
			if session == nil {
				lastErr = fmt.Errorf("MCP %s 重连后仍无会话", serverName)
				continue
			}
		}

		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		})
		if err == nil {
			return extractTextContent(result), nil
		}

		lastErr = err
		if !isMCPConnectionError(err) {
			return "", fmt.Errorf("MCP call %s/%s: %w", serverName, toolName, err)
		}

		log.Printf("[MCP] %s/%s 调用失败 (attempt %d/%d): %v", serverName, toolName, attempt, mcpMaxRetries, err)
		if attempt < mcpMaxRetries {
			_ = b.reconnect(ctx, serverName)
			b.retryWait(ctx, attempt)
		}
	}
	return "", fmt.Errorf("MCP call %s/%s 重试 %d 次后失败: %w", serverName, toolName, mcpMaxRetries, lastErr)
}

// getSession 获取指定 server 的当前 session（线程安全）
func (b *MCPBridge) getSession(serverName string) *mcp.ClientSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[serverName]
}

// reconnect 关闭旧连接并重新建立连接
func (b *MCPBridge) reconnect(ctx context.Context, serverName string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	cfg, ok := b.configs[serverName]
	if !ok {
		return fmt.Errorf("无 %s 的配置信息，无法重连", serverName)
	}

	// 关闭旧 session
	if old, ok := b.sessions[serverName]; ok {
		_ = old.Close()
		delete(b.sessions, serverName)
	}

	session, err := b.dial(ctx, cfg)
	if err != nil {
		return err
	}

	b.sessions[serverName] = session
	log.Printf("[MCP] %s 重连成功", serverName)
	return nil
}

// retryWait 带抖动的等待
func (b *MCPBridge) retryWait(ctx context.Context, attempt int) {
	base := mcpRetryBaseWait * time.Duration(1<<(attempt-1)) // 2s, 4s, 8s
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	wait := base + jitter

	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

// isMCPConnectionError 判断是否为连接类错误（应触发重连）
func isMCPConnectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	connectionPatterns := []string{
		"connection refused",
		"connection reset",
		"broken pipe",
		"eof",
		"transport",
		"dial tcp",
		"no such host",
		"timeout",
		"deadline exceeded",
		"use of closed",
		"session closed",
		"write: broken pipe",
		"read: connection reset",
		"i/o timeout",
		"wsarecv",     // Windows 网络错误
		"wsasend",     // Windows 网络错误
		"connectex",   // Windows 连接错误
	}
	for _, p := range connectionPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// extractTextContent 从 MCP CallToolResult 提取文本内容
func extractTextContent(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		} else {
			data, _ := json.Marshal(content)
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

// Disconnect 断开指定 MCP Server 并移除其工具
func (b *MCPBridge) Disconnect(name string, registry *Registry) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	session, ok := b.sessions[name]
	if !ok {
		return fmt.Errorf("MCP server %s 未连接", name)
	}

	for _, toolName := range b.serverTools[name] {
		delete(b.toolMap, toolName)
		registry.Unregister(toolName)
	}
	delete(b.serverTools, name)
	delete(b.configs, name)

	_ = session.Close()
	delete(b.sessions, name)
	return nil
}

// Connected 返回已连接的 MCP Server 名称列表
func (b *MCPBridge) Connected() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.sessions))
	for name := range b.sessions {
		names = append(names, name)
	}
	return names
}

// Close 关闭所有 MCP 连接
func (b *MCPBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, session := range b.sessions {
		_ = session.Close()
		delete(b.sessions, name)
	}
}
