package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

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

	// 读取配置 - 支持多种 LLM 提供方
	provider := os.Getenv("GOCLAW_PROVIDER") // "claude", "openai", "mimo", ...
	model := os.Getenv("GOCLAW_MODEL")
	tavilyKey := os.Getenv("TAVILY_API_KEY")
	smitheryKey := os.Getenv("SMITHERY_API_KEY")

	var apiKey, baseURL string

	// 根据 provider 选择对应的 API Key 和 Base URL
	switch provider {
	case "claude", "":
		if claudeKey := os.Getenv("ANTHROPIC_API_KEY"); claudeKey != "" {
			provider = "claude"
			apiKey = claudeKey
			baseURL = os.Getenv("ANTHROPIC_BASE_URL")
			if model == "" {
				model = "claude-sonnet-4-20250514"
			}
		}
	case "mimo":
		apiKey = os.Getenv("MIMO_API_KEY")
		baseURL = "https://api.xiaomimimo.com/v1"
		if model == "" {
			model = "mimo-v2-pro"
		}
	case "minimax":
		apiKey = os.Getenv("MINIMAX_API_KEY")
		baseURL = os.Getenv("MINIMAX_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.minimax.chat/v1"
		}
		if model == "" {
			model = "MiniMax-M2.7"
		}
	case "siliconflow":
		apiKey = os.Getenv("SILICONFLOW_API_KEY")
		baseURL = "https://api.siliconflow.cn/v1"
		if model == "" {
			model = "Pro/MiniMaxAI/MiniMax-M2.5"
		}
	default: // openai 兼容 (deepseek, doubao, ollama, ...)
		apiKey = os.Getenv("OPENAI_API_KEY")
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}

	if apiKey == "" {
		fmt.Printf("%s请设置 API Key 环境变量:%s\n", colorRed, colorReset)
		fmt.Println("  Claude:      GOCLAW_PROVIDER=claude      + ANTHROPIC_API_KEY")
		fmt.Println("  MiMo:        GOCLAW_PROVIDER=mimo        + MIMO_API_KEY")
		fmt.Println("  MiniMax:     GOCLAW_PROVIDER=minimax     + MINIMAX_API_KEY")
		fmt.Println("  SiliconFlow: GOCLAW_PROVIDER=siliconflow + SILICONFLOW_API_KEY")
		fmt.Println("  OpenAI 兼容: GOCLAW_PROVIDER=openai      + OPENAI_API_KEY + OPENAI_BASE_URL")
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

	// 注册 Smithery 市场搜索工具
	if smitheryKey != "" {
		registry.Register(tools.NewMCPMarketplaceSearchTool(smitheryKey))
	}

	// 连接 MCP Servers
	mcpBridge := connectMCPServers(registry)
	if mcpBridge != nil {
		defer mcpBridge.Close()
	} else {
		mcpBridge = tools.NewMCPBridge()
	}
	registry.SetMCPBridge(mcpBridge)

	agentCfg := agent.Config{
		Provider: provider,
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
	}

	// 构建重试 + 凭证池配置
	retryCfg := &agent.RetryConfig{MaxAttempts: 3}

	// 加载额外 API Key（逗号分隔，如 GOCLAW_API_KEYS=key1,key2,key3）
	if extraKeys := os.Getenv("GOCLAW_API_KEYS"); extraKeys != "" {
		pool := agent.NewCredentialPool(agent.StrategyRoundRobin)
		pool.AddKey(apiKey, provider, baseURL) // 主 key
		for _, k := range strings.Split(extraKeys, ",") {
			k = strings.TrimSpace(k)
			if k != "" && k != apiKey {
				pool.AddKey(k, provider, baseURL)
			}
		}
		if pool.Size() > 1 {
			log.Printf("🔑 凭证池已加载 %d 个 API Key", pool.Size())
		}
		retryCfg.Pool = pool
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

		// 上下文窗口大小
		contextLength := 128000
		if cl := os.Getenv("GOCLAW_CONTEXT_LENGTH"); cl != "" {
			if v, err := fmt.Sscanf(cl, "%d", &contextLength); err != nil || v != 1 {
				contextLength = 128000
			}
		}

		// 语音转文字配置
		sttCfg := gateway.STTConfig{
			BaseURL: os.Getenv("GOCLAW_STT_BASE_URL"),
			APIKey:  os.Getenv("GOCLAW_STT_API_KEY"),
			Model:   os.Getenv("GOCLAW_STT_MODEL"),
		}
		if sttCfg.APIKey == "" {
			sttCfg.APIKey = apiKey // 默认复用主 API Key
		}
		if sttCfg.Enabled() {
			log.Printf("   STT: %s (模型: %s)", sttCfg.BaseURL, sttCfg.Model)
		}

		// 技能自学习配置
		nudgeInterval := 8
		if ni := os.Getenv("GOCLAW_SKILL_NUDGE_INTERVAL"); ni != "" {
			if v, err := fmt.Sscanf(ni, "%d", &nudgeInterval); err != nil || v != 1 {
				nudgeInterval = 8
			}
		}
		var qqSkillLearnerCfg *agent.SkillLearnerConfig
		if nudgeInterval > 0 {
			qqSkillLearnerCfg = &agent.SkillLearnerConfig{
				NudgeInterval: nudgeInterval,
				SkillsDir:     "skills",
			}
		}

		bot := gateway.NewQQBot(gateway.QQBotConfig{
			WebSocketURL:    qqWS,
			SelfID:          qqSelfID,
			AdminIDs:        adminIDs,
			AgentCfg:        agentCfg,
			Registry:        registry,
			MemStore:        store,
			StickersDir:     os.Getenv("GOCLAW_STICKERS_DIR"),
			ContextLength:   contextLength,
			RetryConfig:     retryCfg,
			SkillLearnerCfg: qqSkillLearnerCfg,
			STTConfig:       sttCfg,
		})

		// 优雅关闭：监听 SIGINT/SIGTERM
		ctx, cancel := context.WithCancel(context.Background())
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			log.Printf("[QQ] 收到信号 %v，正在关闭...", sig)
			cancel()
		}()

		if err := bot.Run(ctx); err != nil && err != context.Canceled {
			log.Fatalf("QQ 机器人退出: %v", err)
		}
		return
	}

	// CLI 模式
	ag := agent.NewAgent(agentCfg, registry, memMgr)
	ag.SetRetryConfig(retryCfg)

	// 配置智能模型路由（可选）
	if simpleModel := os.Getenv("GOCLAW_SIMPLE_MODEL"); simpleModel != "" {
		routerCfg := agent.RouterConfig{
			SimpleModel:  simpleModel,
			ComplexModel: model, // 复杂问题用主模型
		}
		if sp := os.Getenv("GOCLAW_SIMPLE_PROVIDER"); sp != "" {
			routerCfg.SimpleProvider = sp
		}
		if sb := os.Getenv("GOCLAW_SIMPLE_BASE_URL"); sb != "" {
			routerCfg.SimpleBaseURL = sb
		}
		ag.SetRouter(agent.NewModelRouter(routerCfg))
		log.Printf("🧠 智能路由已启用: 简单→%s, 复杂→%s", simpleModel, model)
	}

	// LLM caller（记忆精炼 + 上下文压缩共用）
	llmCaller := func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(agentCfg, tools.NewRegistry(), memory.NewManager(memory.NewStore(memDir), 999))
		return tempAgent.Run(ctx, user)
	}
	memMgr.SetLLMCaller(llmCaller)

	// 设置上下文压缩器
	contextLength := 128000 // 默认上下文窗口
	if cl := os.Getenv("GOCLAW_CONTEXT_LENGTH"); cl != "" {
		if v, err := fmt.Sscanf(cl, "%d", &contextLength); err != nil || v != 1 {
			contextLength = 128000
		}
	}
	compressor := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: contextLength,
	}, llmCaller)
	ag.SetCompressor(compressor)

	// 技能自学习（可选，默认开启）
	nudgeInterval := 8
	if ni := os.Getenv("GOCLAW_SKILL_NUDGE_INTERVAL"); ni != "" {
		if v, err := fmt.Sscanf(ni, "%d", &nudgeInterval); err != nil || v != 1 {
			nudgeInterval = 8
		}
	}
	if nudgeInterval > 0 {
		learner := agent.NewSkillLearner(agent.SkillLearnerConfig{
			NudgeInterval: nudgeInterval,
			SkillsDir:     "skills",
		}, agentCfg, registry, store)
		ag.SetSkillLearner(learner)
	}

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
	capabilities := []string{"文件操作", "Shell/进程", "网页抓取", "HTTP请求", "JSON解析", "定时提醒", "MCP管理"}
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
	fmt.Println("  GOCLAW_PROVIDER      - 提供方 (claude/mimo/minimax/siliconflow/openai)")
	fmt.Println("  ANTHROPIC_API_KEY    - Claude API Key")
	fmt.Println("  ANTHROPIC_BASE_URL   - Claude API 代理地址")
	fmt.Println("  MIMO_API_KEY         - 小米 MiMo API Key")
	fmt.Println("  MINIMAX_API_KEY      - MiniMax API Key")
	fmt.Println("  MINIMAX_BASE_URL     - MiniMax API 地址 (默认 api.minimax.chat)")
	fmt.Println("  SILICONFLOW_API_KEY  - 硅基流动 API Key")
	fmt.Println("  OPENAI_API_KEY       - OpenAI 兼容 API Key")
	fmt.Println("  OPENAI_BASE_URL      - API 地址 (DeepSeek/豆包/Ollama)")
	fmt.Println("  GOCLAW_MODEL         - 模型名称")
	fmt.Println("  TAVILY_API_KEY       - Tavily 搜索 API Key")
	fmt.Println("  SMITHERY_API_KEY     - Smithery MCP 市场 API Key (可选)")
	fmt.Printf("\n%sQQ 机器人:%s\n", colorYellow, colorReset)
	fmt.Println("  GOCLAW_QQ_WS       - NapCatQQ WebSocket 地址 (如 ws://127.0.0.1:3001)")
	fmt.Println("  GOCLAW_QQ_SELF_ID  - 机器人 QQ 号")
	fmt.Println("  GOCLAW_QQ_ADMINS   - 允许使用的 QQ 号 (逗号分隔，空则不限)")
	fmt.Println()
	fmt.Println("  语音转文字 (可选):")
	fmt.Println("  GOCLAW_STT_BASE_URL - STT API 地址 (如 https://api.openai.com/v1)")
	fmt.Println("  GOCLAW_STT_API_KEY  - STT API Key (默认复用 GOCLAW_API_KEY)")
	fmt.Println("  GOCLAW_STT_MODEL    - STT 模型名 (默认 whisper-1)")
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
// 支持 stdio（command+args）、sse（endpoint）、streamable_http（endpoint）三种传输
func connectMCPServers(registry *tools.Registry) *tools.MCPBridge {
	data, err := os.ReadFile("mcp_servers.json")
	if err != nil {
		return nil
	}

	var config struct {
		MCPServers map[string]struct {
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			Endpoint  string            `json:"endpoint"`
			Transport string            `json:"transport"`
			APIKey    string            `json:"api_key"`
			ToolNames []string          `json:"tool_names"`
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
			Name:      name,
			Command:   srv.Command,
			Args:      srv.Args,
			Env:       srv.Env,
			Endpoint:  srv.Endpoint,
			Transport: srv.Transport,
			APIKey:    srv.APIKey,
			ToolNames: srv.ToolNames,
		}, registry)
		if err != nil {
			fmt.Printf("  %sMCP %s 连接失败: %v%s\n", colorRed, name, err, colorReset)
		}
	}

	return bridge
}