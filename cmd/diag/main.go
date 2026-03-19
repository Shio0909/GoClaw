package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// 模拟 Claude API 请求体结构
type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system"`
	Messages  []claudeMessage `json:"messages"`
	Tools     []claudeTool    `json:"tools"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

func main() {
	loadEnvFile(".env")
	tools.InitSandbox()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)

	// 模拟 MCP 连接
	bridge := connectMCP(registry)
	if bridge != nil {
		defer bridge.Close()
	} else {
		bridge = tools.NewMCPBridge()
	}
	registry.SetMCPBridge(bridge)

	// 可选工具
	if key := os.Getenv("TAVILY_API_KEY"); key != "" {
		registry.Register(tools.NewWebSearchTool(key))
	}
	if key := os.Getenv("SMITHERY_API_KEY"); key != "" {
		registry.Register(tools.NewMCPMarketplaceSearchTool(key))
	}

	// 构建 system prompt
	store := memory.NewStore("memory_data")
	memMgr := memory.NewManager(store, 999)
	agent.SkillsDir = "skills"
	sysPrompt, _ := agent.BuildSystemPrompt(memMgr, registry)
	sysPrompt += "\n\n" + qqPrompt

	fmt.Printf("=== 请求体大小诊断 ===\n\n")
	fmt.Printf("System prompt: %d 字节 (%.1f KB)\n", len(sysPrompt), float64(len(sysPrompt))/1024)

	// 构建工具定义
	var toolDefs []claudeTool
	names := registry.Names()
	fmt.Printf("工具数量: %d\n\n", len(names))

	for _, name := range names {
		t, _ := registry.Get(name)
		if t == nil {
			continue
		}
		schema := map[string]interface{}{
			"type": "object",
		}
		props := map[string]interface{}{}
		var required []string
		for _, p := range t.Parameters {
			props[p.Name] = map[string]interface{}{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}
		if len(props) > 0 {
			schema["properties"] = props
		}
		if len(required) > 0 {
			schema["required"] = required
		}

		td := claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
		toolDefs = append(toolDefs, td)

		data, _ := json.Marshal(td)
		fmt.Printf("  %-30s %5d 字节  %s\n", name, len(data), truncate(t.Description, 40))
	}

	toolsJSON, _ := json.Marshal(toolDefs)
	fmt.Printf("\n工具定义总计: %d 字节 (%.1f KB)\n", len(toolsJSON), float64(len(toolsJSON))/1024)

	// 模拟完整请求
	req := claudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4096,
		System:    sysPrompt,
		Messages:  []claudeMessage{{Role: "user", Content: "你读一下我的e盘里的learngo文件夹"}},
		Tools:     toolDefs,
	}
	reqJSON, _ := json.Marshal(req)
	fmt.Printf("\n=== 总计 ===\n")
	fmt.Printf("完整请求体: %d 字节 (%.1f KB)\n", len(reqJSON), float64(len(reqJSON))/1024)
}

const qqPrompt = `你正在 QQ 聊天中回复消息。`

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func loadEnvFile(path string) {
	data, _ := os.ReadFile(path)
	if data == nil {
		return
	}
	for _, line := range splitLines(string(data)) {
		if line == "" || line[0] == '#' {
			continue
		}
		for i, c := range line {
			if c == '=' {
				k, v := line[:i], line[i+1:]
				if os.Getenv(k) == "" {
					os.Setenv(k, v)
				}
				break
			}
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func connectMCP(registry *tools.Registry) *tools.MCPBridge {
	data, err := os.ReadFile("mcp_servers.json")
	if err != nil {
		return nil
	}
	var config struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	if len(config.MCPServers) == 0 {
		return nil
	}
	bridge := tools.NewMCPBridge()
	ctx := context.Background()
	for name, srv := range config.MCPServers {
		bridge.Connect(ctx, tools.MCPServerConfig{
			Name: name, Command: srv.Command, Args: srv.Args, Env: srv.Env,
		}, registry)
	}
	return bridge
}
