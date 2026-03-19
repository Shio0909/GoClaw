package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/gateway"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// ANSI 颜色
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)

func main() {
	// 自动加载 .env 文件
	loadEnvFile(".env")

	// 读取配置 - 支持 OpenAI 兼容 和 Claude 两种模式
	provider := os.Getenv("GOCLAW_PROVIDER") // "openai" 或 "claude"
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := os.Getenv("OPENAI_BASE_URL")
	model := os.Getenv("GOCLAW_MODEL")
	tavilyKey := os.Getenv("TAVILY_API_KEY")

	// Claude 模式
	if provider == "claude" || provider == "" {
		if claudeKey := os.Getenv("ANTHROPIC_API_KEY"); claudeKey != "" {
			provider = "claude"
			apiKey = claudeKey
			baseURL = os.Getenv("ANTHROPIC_BASE_URL")
			if model == "" {
				model = "claude-sonnet-4-20250514"
			}
		}
	}

	if apiKey == "" {
		fmt.Printf("%s请设置 API Key 环境变量:%s\n", colorRed, colorReset)
		fmt.Println("  OpenAI 兼容: OPENAI_API_KEY + OPENAI_BASE_URL")
		fmt.Println("  Claude:      ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL (可选)")
		fmt.Println()
		fmt.Println("可选: TAVILY_API_KEY (网络搜索), GOCLAW_MODEL (模型名)")
		os.Exit(1)
	}

	if provider == "" {
		provider = "openai"
	}
	if baseURL == "" && provider == "openai" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o-mini"
	}

	// 初始化记忆
	memDir := findMemoryDir()
	store := memory.NewStore(memDir)
	memMgr := memory.NewManager(store, 10)

	// 初始化工具
	tools.InitSandbox()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)

	// 设置技能目录
	agent.SkillsDir = "skills"

	// 注册 Tavily 搜索工具
	if tavilyKey != "" {
		registry.Register(tools.NewWebSearchTool(tavilyKey))
	}

	// 连接 MCP Servers
	mcpBridge := connectMCPServers(registry)
	if mcpBridge != nil {
		defer mcpBridge.Close()
	}

	agentCfg := agent.Config{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
	}

	// QQ 机器人模式
	qqWS := os.Getenv("GOCLAW_QQ_WS")
	qqSelfID := os.Getenv("GOCLAW_QQ_SELF_ID")
	if qqWS != "" {
		log.Printf("🐾 GoClaw QQ 机器人启动中...")
		log.Printf("   WebSocket: %s | 模型: %s", qqWS, model)

		var adminIDs []string
		if ids := os.Getenv("GOCLAW_QQ_ADMINS"); ids != "" {
			adminIDs = strings.Split(ids, ",")
		}

		bot := gateway.NewQQBot(gateway.QQBotConfig{
			WebSocketURL: qqWS,
			SelfID:       qqSelfID,
			AdminIDs:     adminIDs,
			AgentCfg:     agentCfg,
			Registry:     registry,
			MemStore:     store,
		})
		if err := bot.Run(context.Background()); err != nil {
			log.Fatalf("QQ 机器人退出: %v", err)
		}
		return
	}

	// CLI 模式
	ag := agent.NewAgent(agentCfg, registry, memMgr)

	// 注入 LLM caller 给记忆管理器
	memMgr.SetLLMCaller(func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(agentCfg, tools.NewRegistry(), memory.NewManager(memory.NewStore(memDir), 999))
		return tempAgent.Run(ctx, user)
	})

	// 打印欢迎信息
	printBanner(provider, model, baseURL, tavilyKey != "")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	ctx := context.Background()

	for {
		fmt.Printf("%s%sYou >%s ", colorBold, colorGreen, colorReset)
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// 内置命令
		switch {
		case input == "/quit" || input == "/exit":
			fmt.Printf("%s再见！%s\n", colorCyan, colorReset)
			return
		case input == "/memory":
			showMemoryStatus(store)
			continue
		case input == "/help":
			showHelp()
			continue
		case input == "/clear":
			ag.ClearHistory()
			fmt.Printf("%s对话历史已清空%s\n\n", colorYellow, colorReset)
			continue
		}

		// 调用 Agent（流式输出）
		fmt.Printf("%s%sGoClaw >%s ", colorBold, colorCyan, colorReset)
		stream, err := ag.RunStream(ctx, input)
		if err != nil {
			fmt.Printf("\n%sError: %v%s\n\n", colorRed, err, colorReset)
			continue
		}

		// 逐 chunk 读取并打印
		var fullContent strings.Builder
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				fmt.Printf("\n%sStream error: %v%s\n", colorRed, err, colorReset)
				break
			}
			if msg != nil && msg.Content != "" {
				fmt.Print(msg.Content)
				fullContent.WriteString(msg.Content)
			}
		}
		fmt.Print("\n\n")

		// 将完整回复加入历史
		if fullContent.Len() > 0 {
			ag.AppendAssistantMessage(ctx, fullContent.String())
		}
	}
}

func printBanner(provider, model, baseURL string, hasSearch bool) {
	fmt.Println()
	fmt.Printf("  %s%s🐾 GoClaw%s - Go 语言 AI 助手\n", colorBold, colorCyan, colorReset)
	fmt.Printf("  %s模型: %s | 后端: %s%s\n", colorDim, model, provider, colorReset)
	if baseURL != "" {
		fmt.Printf("  %sAPI:  %s%s\n", colorDim, baseURL, colorReset)
	}

	// 显示能力
	capabilities := []string{"文件操作", "Shell/进程", "网页抓取", "HTTP请求", "JSON解析", "定时提醒"}
	if hasSearch {
		capabilities = append(capabilities, "网络搜索")
	}
	fmt.Printf("  %s工具: %s%s\n", colorDim, strings.Join(capabilities, " | "), colorReset)
	fmt.Printf("  %s命令: /help /memory /clear /quit%s\n", colorDim, colorReset)
	fmt.Println()
}

func showHelp() {
	fmt.Printf("\n%s可用命令:%s\n", colorYellow, colorReset)
	fmt.Println("  /help    - 显示帮助")
	fmt.Println("  /memory  - 查看记忆状态")
	fmt.Println("  /clear   - 清空对话历史")
	fmt.Println("  /quit    - 退出")
	fmt.Printf("\n%s环境变量:%s\n", colorYellow, colorReset)
	fmt.Println("  OPENAI_API_KEY     - OpenAI 兼容 API Key")
	fmt.Println("  OPENAI_BASE_URL    - API 地址 (DeepSeek/豆包/Ollama)")
	fmt.Println("  ANTHROPIC_API_KEY  - Claude API Key")
	fmt.Println("  ANTHROPIC_BASE_URL - Claude API 代理地址")
	fmt.Println("  GOCLAW_MODEL       - 模型名称")
	fmt.Println("  TAVILY_API_KEY     - Tavily 搜索 API Key")
	fmt.Printf("\n%sQQ 机器人:%s\n", colorYellow, colorReset)
	fmt.Println("  GOCLAW_QQ_WS       - NapCatQQ WebSocket 地址 (如 ws://127.0.0.1:3001)")
	fmt.Println("  GOCLAW_QQ_SELF_ID  - 机器人 QQ 号")
	fmt.Println("  GOCLAW_QQ_ADMINS   - 允许使用的 QQ 号 (逗号分隔，空则不限)")
	fmt.Println()
}

func showMemoryStatus(store *memory.Store) {
	soul, _ := store.ReadSoul()
	user, _ := store.ReadUser()
	mem, _ := store.ReadMemory()
	logs, _ := store.ReadTodayLogs()

	fmt.Printf("\n%s--- 记忆状态 ---%s\n", colorYellow, colorReset)
	fmt.Printf("  Soul:     %s%d 字符%s\n", colorCyan, len(soul), colorReset)
	fmt.Printf("  User:     %s%d 字符%s\n", colorCyan, len(user), colorReset)
	fmt.Printf("  Memory:   %s%d 字符%s\n", colorCyan, len(mem), colorReset)
	fmt.Printf("  今日日志: %s%d 条%s\n", colorCyan, len(logs), colorReset)
	fmt.Println()
}

func findMemoryDir() string {
	// 优先用当前目录下的 memory_data
	if _, err := os.Stat("memory_data"); err == nil {
		return "memory_data"
	}
	// 否则用可执行文件旁边的
	exePath, _ := os.Executable()
	return filepath.Join(filepath.Dir(exePath), "memory_data")
}

// loadEnvFile 简易 .env 文件加载（不覆盖已有环境变量）
func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		// 不覆盖已有的环境变量
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// connectMCPServers 从 mcp_servers.json 加载并连接 MCP Servers
func connectMCPServers(registry *tools.Registry) *tools.MCPBridge {
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
		fmt.Printf("%sMCP 配置解析失败: %v%s\n", colorRed, err, colorReset)
		return nil
	}

	if len(config.MCPServers) == 0 {
		return nil
	}

	bridge := tools.NewMCPBridge()
	ctx := context.Background()

	for name, srv := range config.MCPServers {
		err := bridge.Connect(ctx, tools.MCPServerConfig{
			Name:    name,
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
		}, registry)
		if err != nil {
			fmt.Printf("  %sMCP %s 连接失败: %v%s\n", colorRed, name, err, colorReset)
		}
	}

	return bridge
}