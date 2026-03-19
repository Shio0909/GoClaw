package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// MCPServerEntry 精选 MCP Server 条目
type MCPServerEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	EnvKeys     []string `json:"env_keys"` // 需要用户提供的环境变量名
	Tags        []string `json:"tags"`
}

// curatedMCPServers 精选 MCP Server 列表
var curatedMCPServers = []MCPServerEntry{
	{Name: "filesystem", Description: "文件系统操作（读写、搜索、目录管理）", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "{path}"}, Tags: []string{"file", "fs", "文件"}},
	{Name: "github", Description: "GitHub 仓库、Issue、PR 管理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, EnvKeys: []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}, Tags: []string{"git", "github", "代码"}},
	{Name: "gitlab", Description: "GitLab 项目管理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-gitlab"}, EnvKeys: []string{"GITLAB_PERSONAL_ACCESS_TOKEN", "GITLAB_API_URL"}, Tags: []string{"git", "gitlab", "代码"}},
	{Name: "postgres", Description: "PostgreSQL 数据库查询与管理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-postgres", "{connection_string}"}, Tags: []string{"database", "db", "sql", "postgres", "数据库"}},
	{Name: "sqlite", Description: "SQLite 数据库操作", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-sqlite", "{db_path}"}, Tags: []string{"database", "db", "sql", "sqlite", "数据库"}},
	{Name: "memory", Description: "知识图谱式持久记忆", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-memory"}, Tags: []string{"memory", "知识", "记忆"}},
	{Name: "brave-search", Description: "Brave 搜索引擎", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-brave-search"}, EnvKeys: []string{"BRAVE_API_KEY"}, Tags: []string{"search", "搜索", "brave"}},
	{Name: "google-maps", Description: "Google Maps 地理位置服务", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-google-maps"}, EnvKeys: []string{"GOOGLE_MAPS_API_KEY"}, Tags: []string{"map", "地图", "google"}},
	{Name: "slack", Description: "Slack 消息与频道管理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-slack"}, EnvKeys: []string{"SLACK_BOT_TOKEN", "SLACK_TEAM_ID"}, Tags: []string{"slack", "消息", "chat"}},
	{Name: "puppeteer", Description: "浏览器自动化（截图、爬取、交互）", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-puppeteer"}, Tags: []string{"browser", "浏览器", "爬虫", "puppeteer"}},
	{Name: "fetch", Description: "HTTP 请求与网页内容抓取", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-fetch"}, Tags: []string{"http", "fetch", "网页", "抓取"}},
	{Name: "sequential-thinking", Description: "动态思维链推理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-sequential-thinking"}, Tags: []string{"thinking", "推理", "思考"}},
	{Name: "everything", Description: "MCP 测试/演示服务器", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-everything"}, Tags: []string{"test", "demo", "测试"}},
	{Name: "docker", Description: "Docker 容器管理", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-docker"}, Tags: []string{"docker", "容器", "container"}},
	{Name: "time", Description: "时间与时区转换", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-time"}, Tags: []string{"time", "时间", "时区"}},
}

// trustedPrefixes 可信包前缀白名单
var trustedPrefixes = []string{
	"@modelcontextprotocol/",
}

// isTrustedPackage 检查包是否在白名单中
func isTrustedPackage(name string, args []string) bool {
	// 精选列表中的条目视为可信
	for _, s := range curatedMCPServers {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	// 检查 args 中是否包含可信前缀
	for _, arg := range args {
		for _, prefix := range trustedPrefixes {
			if strings.Contains(arg, prefix) {
				return true
			}
		}
	}
	return false
}

// buildSecurityReview 生成安全审查信息
func buildSecurityReview(name, command string, args []string, envMap map[string]string) string {
	var sb strings.Builder
	sb.WriteString("⚠️ 安全审查 — 第三方 MCP Server\n\n")
	sb.WriteString(fmt.Sprintf("名称: %s\n", name))
	sb.WriteString(fmt.Sprintf("来源: %s %s\n", command, strings.Join(args, " ")))
	sb.WriteString("类型: 非官方认证包\n")
	if len(envMap) > 0 {
		keys := make([]string, 0, len(envMap))
		for k := range envMap {
			keys = append(keys, k)
		}
		sb.WriteString(fmt.Sprintf("环境变量: %s\n", strings.Join(keys, ", ")))
	}
	sb.WriteString("\n⚠️ 风险提示：\n")
	sb.WriteString("  - 该包将作为子进程运行在本机，拥有完整系统权限\n")
	sb.WriteString("  - 可以读写文件、访问网络、执行命令\n")
	sb.WriteString("  - 可以读取传入的环境变量（包括 API Key）\n")
	sb.WriteString("\n请将以上信息展示给用户。用户确认后，再次调用 mcp_install 并设置 confirmed=\"true\"。")
	return sb.String()
}

// NewMCPSearchTool 搜索可用 MCP Server
func NewMCPSearchTool() *ToolDef {
	return &ToolDef{
		Name:        "mcp_search",
		Description: "搜索可用的 MCP Server。从精选列表中按关键词过滤，返回名称、描述和所需配置。不传 query 则返回全部",
		Parameters: []ParamDef{
			{Name: "query", Type: "string", Description: "搜索关键词（匹配名称、描述、标签）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			query, _ := args["query"].(string)
			query = strings.ToLower(strings.TrimSpace(query))

			var results []MCPServerEntry
			for _, s := range curatedMCPServers {
				if query == "" || matchEntry(s, query) {
					results = append(results, s)
				}
			}

			if len(results) == 0 {
				return fmt.Sprintf("精选列表中未找到匹配 \"%s\" 的 MCP Server。你可以用 web_search 搜索更多，或直接用 mcp_install 手动指定 command/args 安装。", query), nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("找到 %d 个 MCP Server：\n\n", len(results)))
			for _, s := range results {
				sb.WriteString(fmt.Sprintf("• %s — %s\n", s.Name, s.Description))
				if len(s.EnvKeys) > 0 {
					sb.WriteString(fmt.Sprintf("  需要环境变量: %s\n", strings.Join(s.EnvKeys, ", ")))
				}
			}
			return sb.String(), nil
		},
	}
}

func matchEntry(s MCPServerEntry, query string) bool {
	if strings.Contains(strings.ToLower(s.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(s.Description), query) {
		return true
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

// NewMCPInstallTool 安装 MCP Server（写入配置 + 运行时连接）
func NewMCPInstallTool(registry *Registry) *ToolDef {
	return &ToolDef{
		Name:        "mcp_install",
		Description: "安装并连接一个 MCP Server。写入 mcp_servers.json 配置并立即连接，当前对话即可使用新工具",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "MCP Server 名称（如 github、filesystem）", Required: true},
			{Name: "command", Type: "string", Description: "启动命令（如 npx、uvx、node）", Required: false},
			{Name: "args", Type: "string", Description: "命令参数，JSON 数组格式（如 [\"-y\",\"@modelcontextprotocol/server-github\"]）", Required: false},
			{Name: "env", Type: "string", Description: "环境变量，JSON 对象格式（如 {\"GITHUB_PERSONAL_ACCESS_TOKEN\":\"xxx\"}）", Required: false},
			{Name: "confirmed", Type: "string", Description: "安全审查确认。非白名单包需要用户确认后设为 \"true\"", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name 为必填")
			}

			command, _ := args["command"].(string)
			argsStr, _ := args["args"].(string)
			envStr, _ := args["env"].(string)

			// 如果没指定 command/args，尝试从精选列表查找
			var cmdArgs []string
			var envMap map[string]string

			if command == "" {
				found := false
				for _, s := range curatedMCPServers {
					if strings.EqualFold(s.Name, name) {
						command = s.Command
						cmdArgs = s.Args
						found = true
						break
					}
				}
				if !found {
					return "", fmt.Errorf("精选列表中未找到 %s，请手动指定 command 和 args", name)
				}
			}

			// 解析 args JSON
			if argsStr != "" {
				if err := json.Unmarshal([]byte(argsStr), &cmdArgs); err != nil {
					return "", fmt.Errorf("args JSON 解析失败: %w", err)
				}
			}

			// 解析 env JSON
			if envStr != "" {
				envMap = make(map[string]string)
				if err := json.Unmarshal([]byte(envStr), &envMap); err != nil {
					return "", fmt.Errorf("env JSON 解析失败: %w", err)
				}
			}

			// 安全审查：非白名单包需要用户确认
			confirmed, _ := args["confirmed"].(string)
			if !isTrustedPackage(name, cmdArgs) && confirmed != "true" {
				return buildSecurityReview(name, command, cmdArgs, envMap), nil
			}

			bridge := registry.GetMCPBridge()
			if bridge == nil {
				return "", fmt.Errorf("MCP Bridge 未初始化")
			}

			// 连接
			cfg := MCPServerConfig{
				Name:    name,
				Command: command,
				Args:    cmdArgs,
				Env:     envMap,
			}
			if err := bridge.Connect(ctx, cfg, registry); err != nil {
				return "", fmt.Errorf("连接失败: %w", err)
			}

			// 写入配置文件
			if err := saveMCPConfig(name, command, cmdArgs, envMap); err != nil {
				return fmt.Sprintf("已连接 %s，但配置保存失败: %v（重启后需重新安装）", name, err), nil
			}

			toolCount := len(bridge.serverTools[name])
			return fmt.Sprintf("已安装并连接 %s（%d 个工具）。配置已保存到 mcp_servers.json，当前对话立即可用。", name, toolCount), nil
		},
	}
}

// NewMCPUninstallTool 卸载 MCP Server
func NewMCPUninstallTool(registry *Registry) *ToolDef {
	return &ToolDef{
		Name:        "mcp_uninstall",
		Description: "卸载一个已安装的 MCP Server。断开连接并从配置中移除",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "要卸载的 MCP Server 名称", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name 为必填")
			}

			bridge := registry.GetMCPBridge()
			if bridge == nil {
				return "", fmt.Errorf("MCP Bridge 未初始化")
			}

			// 断开连接
			if err := bridge.Disconnect(name, registry); err != nil {
				return "", err
			}

			// 从配置文件移除
			if err := removeMCPConfig(name); err != nil {
				return fmt.Sprintf("已断开 %s，但配置移除失败: %v", name, err), nil
			}

			return fmt.Sprintf("已卸载 %s，工具已移除。", name), nil
		},
	}
}

// saveMCPConfig 将 MCP Server 配置追加到 mcp_servers.json
func saveMCPConfig(name, command string, args []string, env map[string]string) error {
	config := loadMCPConfigFile()
	if config == nil {
		config = make(map[string]interface{})
	}

	servers, _ := config["mcpServers"].(map[string]interface{})
	if servers == nil {
		servers = make(map[string]interface{})
	}

	entry := map[string]interface{}{
		"command": command,
		"args":    args,
	}
	if env != nil {
		entry["env"] = env
	} else {
		entry["env"] = map[string]string{}
	}
	servers[name] = entry
	config["mcpServers"] = servers

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("mcp_servers.json", data, 0644)
}

// removeMCPConfig 从 mcp_servers.json 移除指定 server
func removeMCPConfig(name string) error {
	config := loadMCPConfigFile()
	if config == nil {
		return nil
	}

	servers, _ := config["mcpServers"].(map[string]interface{})
	if servers == nil {
		return nil
	}

	delete(servers, name)
	config["mcpServers"] = servers

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("mcp_servers.json", data, 0644)
}

// loadMCPConfigFile 读取 mcp_servers.json
func loadMCPConfigFile() map[string]interface{} {
	data, err := os.ReadFile("mcp_servers.json")
	if err != nil {
		return nil
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	return config
}
