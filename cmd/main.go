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
	"sync"
	"syscall"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/config"
	"github.com/goclaw/goclaw/gateway"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

const version = "0.2.0"

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
	// 自动加载 .env 文件（用于向后兼容）
	loadEnvFile(".env")

	// 解析子命令
	cmd := ""
	configPath := "goclaw.yaml"
	args := os.Args[1:]

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "serve", "cli", "version":
			cmd = args[i]
		case "-c", "--config":
			if i+1 < len(args) {
				i++
				configPath = args[i]
			}
		}
	}

	if cmd == "version" {
		fmt.Printf("GoClaw v%s\n", version)
		return
	}

	// 加载配置（YAML + 环境变量回退）
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	if cfg.Agent.APIKey == "" {
		fmt.Printf("%s请设置 API Key（通过 goclaw.yaml 或环境变量）:%s\n", colorRed, colorReset)
		fmt.Println("  Claude:      GOCLAW_PROVIDER=claude      + ANTHROPIC_API_KEY")
		fmt.Println("  MiniMax:     GOCLAW_PROVIDER=minimax     + MINIMAX_API_KEY")
		fmt.Println("  SiliconFlow: GOCLAW_PROVIDER=siliconflow + SILICONFLOW_API_KEY")
		fmt.Println("  OpenAI 兼容: GOCLAW_PROVIDER=openai      + OPENAI_API_KEY + OPENAI_BASE_URL")
		fmt.Println()
		fmt.Println("或创建 goclaw.yaml 配置文件（参考 goclaw.example.yaml）")
		os.Exit(1)
	}

	// 共享基础设施
	infra := setupInfra(cfg)
	defer infra.cleanup()

	switch cmd {
	case "serve":
		runServe(cfg, infra)
	default:
		// 默认 CLI 模式（向后兼容：无子命令 = CLI）
		// 但如果配置了 QQ Gateway 且没有 HTTP listen，走 QQ 模式
		if cfg.Gateway.QQ != nil && cfg.Gateway.QQ.Enabled && cfg.Server.Listen == "" {
			runServe(cfg, infra)
		} else if cfg.Server.Listen != "" {
			runServe(cfg, infra)
		} else {
			runCLI(cfg, infra)
		}
	}
}

// infra 共享基础设施
type infra struct {
	agentCfg  agent.Config
	registry  *tools.Registry
	memStore  *memory.Store
	memDir    string
	retryCfg  *agent.RetryConfig
	mcpBridge *tools.MCPBridge
}

func (inf *infra) cleanup() {
	if inf.mcpBridge != nil {
		inf.mcpBridge.Close()
	}
}

func setupInfra(cfg *config.Config) *infra {
	// 初始化记忆
	memDir := findMemoryDir()
	store := memory.NewStore(memDir)

	// 初始化工具
	tools.InitSandbox()
	registry := tools.NewRegistry()
	tools.RegisterBuiltins(registry)

	// 设置技能目录
	agent.SkillsDir = cfg.Tools.SkillsDir

	// 注册搜索工具
	if cfg.Tools.TavilyKey != "" {
		registry.Register(tools.NewWebSearchTool(cfg.Tools.TavilyKey))
	}
	if cfg.Tools.SmitheryKey != "" {
		registry.Register(tools.NewMCPMarketplaceSearchTool(cfg.Tools.SmitheryKey))
	}

	// 连接 MCP Servers
	mcpBridge := connectMCPServersFromConfig(cfg, registry)
	if mcpBridge == nil {
		// 回退：尝试旧的 mcp_servers.json
		mcpBridge = connectMCPServers(registry)
	}
	if mcpBridge == nil {
		mcpBridge = tools.NewMCPBridge()
	}
	registry.SetMCPBridge(mcpBridge)

	agentCfg := agent.Config{
		Provider: cfg.Agent.Provider,
		APIKey:   cfg.Agent.APIKey,
		BaseURL:  cfg.Agent.BaseURL,
		Model:    cfg.Agent.Model,
	}

	// 构建重试 + 凭证池配置
	retryCfg := &agent.RetryConfig{MaxAttempts: 3}
	if len(cfg.Agent.APIKeys) > 0 {
		pool := agent.NewCredentialPool(agent.StrategyRoundRobin)
		pool.AddKey(agentCfg.APIKey, agentCfg.Provider, agentCfg.BaseURL)
		for _, k := range cfg.Agent.APIKeys {
			if k != agentCfg.APIKey {
				pool.AddKey(k, agentCfg.Provider, agentCfg.BaseURL)
			}
		}
		if pool.Size() > 1 {
			log.Printf("🔑 凭证池已加载 %d 个 API Key", pool.Size())
		}
		retryCfg.Pool = pool
	}

	return &infra{
		agentCfg:  agentCfg,
		registry:  registry,
		memStore:  store,
		memDir:    memDir,
		retryCfg:  retryCfg,
		mcpBridge: mcpBridge,
	}
}

// runServe 启动 HTTP API + Gateway 服务模式
func runServe(cfg *config.Config, inf *infra) {
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("收到信号 %v，正在关闭...", sig)
		cancel()
	}()

	var gateways []gateway.Gateway

	// HTTP API Server
	if cfg.Server.Listen != "" {
		httpSrv := gateway.NewHTTPServer(gateway.HTTPServerConfig{
			Addr:          cfg.Server.Listen,
			AgentCfg:      inf.agentCfg,
			Registry:      inf.registry,
			MemStore:      inf.memStore,
			RetryConfig:   inf.retryCfg,
			ContextLength: cfg.Agent.ContextLength,
		})
		gateways = append(gateways, httpSrv)
	}

	// QQ Gateway
	if cfg.Gateway.QQ != nil && cfg.Gateway.QQ.Enabled {
		sttCfg := gateway.STTConfig{
			BaseURL: cfg.Gateway.QQ.STT.BaseURL,
			APIKey:  cfg.Gateway.QQ.STT.APIKey,
			Model:   cfg.Gateway.QQ.STT.Model,
		}
		if sttCfg.APIKey == "" {
			sttCfg.APIKey = inf.agentCfg.APIKey
		}

		var skillCfg *agent.SkillLearnerConfig
		if cfg.Tools.SkillNudge > 0 {
			skillCfg = &agent.SkillLearnerConfig{
				NudgeInterval: cfg.Tools.SkillNudge,
				SkillsDir:     cfg.Tools.SkillsDir,
			}
		}

		bot := gateway.NewQQBot(gateway.QQBotConfig{
			WebSocketURL:    cfg.Gateway.QQ.WebSocket,
			SelfID:          cfg.Gateway.QQ.SelfID,
			AdminIDs:        cfg.Gateway.QQ.Admins,
			AgentCfg:        inf.agentCfg,
			Registry:        inf.registry,
			MemStore:        inf.memStore,
			StickersDir:     cfg.Gateway.QQ.StickersDir,
			ContextLength:   cfg.Agent.ContextLength,
			RetryConfig:     inf.retryCfg,
			SkillLearnerCfg: skillCfg,
			STTConfig:       sttCfg,
		})
		gateways = append(gateways, bot)
	}

	if len(gateways) == 0 {
		log.Fatal("serve 模式需要至少一个 gateway 或 HTTP API 监听地址")
	}

	// 启动所有 gateway
	log.Printf("🐾 GoClaw v%s 服务模式启动", version)
	log.Printf("   模型: %s | 后端: %s", inf.agentCfg.Model, inf.agentCfg.Provider)
	for _, gw := range gateways {
		log.Printf("   网关: %s", gw.Name())
	}

	var wg sync.WaitGroup
	for _, gw := range gateways {
		wg.Add(1)
		go func(g gateway.Gateway) {
			defer wg.Done()
			if err := g.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("[%s] 退出: %v", g.Name(), err)
			}
		}(gw)
	}
	wg.Wait()
}

// runCLI 交互式 CLI 模式
func runCLI(cfg *config.Config, inf *infra) {
	memMgr := memory.NewManager(inf.memStore, 10)
	ag := agent.NewAgent(inf.agentCfg, inf.registry, memMgr)
	ag.SetRetryConfig(inf.retryCfg)

	// 智能模型路由
	if cfg.Agent.SimpleModel != "" {
		routerCfg := agent.RouterConfig{
			SimpleModel:    cfg.Agent.SimpleModel,
			ComplexModel:   cfg.Agent.Model,
			SimpleProvider: cfg.Agent.SimpleProvider,
			SimpleBaseURL:  cfg.Agent.SimpleBaseURL,
		}
		ag.SetRouter(agent.NewModelRouter(routerCfg))
		log.Printf("🧠 智能路由已启用: 简单→%s, 复杂→%s", cfg.Agent.SimpleModel, cfg.Agent.Model)
	}

	// LLM caller（记忆精炼 + 上下文压缩共用）
	llmCaller := func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(inf.agentCfg, tools.NewRegistry(), memory.NewManager(memory.NewStore(inf.memDir), 999))
		return tempAgent.Run(ctx, user)
	}
	memMgr.SetLLMCaller(llmCaller)

	// 上下文压缩器
	compressor := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: cfg.Agent.ContextLength,
	}, llmCaller)
	ag.SetCompressor(compressor)

	// 技能自学习
	if cfg.Tools.SkillNudge > 0 {
		learner := agent.NewSkillLearner(agent.SkillLearnerConfig{
			NudgeInterval: cfg.Tools.SkillNudge,
			SkillsDir:     cfg.Tools.SkillsDir,
		}, inf.agentCfg, inf.registry, inf.memStore)
		ag.SetSkillLearner(learner)
	}

	// 打印欢迎信息
	printBanner(cfg.Agent.Provider, cfg.Agent.Model, cfg.Agent.BaseURL, cfg.Tools.TavilyKey != "")

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

		switch {
		case input == "/quit" || input == "/exit":
			fmt.Printf("%s再见！%s\n", colorCyan, colorReset)
			return
		case input == "/memory":
			showMemoryStatus(inf.memStore)
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

		filter := &agent.ThinkFilter{}
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
				filtered := filter.Process(msg.Content)
				if filtered != "" {
					fmt.Print(filtered)
					fullContent.WriteString(filtered)
				}
			}
		}
		if remaining := filter.Flush(); remaining != "" {
			fmt.Print(remaining)
			fullContent.WriteString(remaining)
		}
		fmt.Print("\n\n")

		if fullContent.Len() > 0 {
			ag.AppendAssistantMessage(ctx, fullContent.String())
		}
	}
}

func printBanner(provider, model, baseURL string, hasSearch bool) {
	fmt.Println()
	fmt.Printf("  %s%s🐾 GoClaw v%s%s - Go AI Agent Runtime\n", colorBold, colorCyan, version, colorReset)
	fmt.Printf("  %s模型: %s | 后端: %s%s\n", colorDim, model, provider, colorReset)
	if baseURL != "" {
		fmt.Printf("  %sAPI:  %s%s\n", colorDim, baseURL, colorReset)
	}

	capabilities := []string{"文件操作", "Shell/进程", "网页抓取", "HTTP请求", "JSON解析", "定时提醒", "MCP管理"}
	if hasSearch {
		capabilities = append(capabilities, "网络搜索")
	}
	fmt.Printf("  %s工具: %s%s\n", colorDim, strings.Join(capabilities, " | "), colorReset)
	fmt.Printf("  %s命令: /help /memory /clear /quit%s\n", colorDim, colorReset)
	fmt.Println()
}

func showHelp() {
	fmt.Printf("\n%s子命令:%s\n", colorYellow, colorReset)
	fmt.Println("  goclaw cli     - 交互式 CLI 模式（默认）")
	fmt.Println("  goclaw serve   - 启动 HTTP API + Gateway 服务")
	fmt.Println("  goclaw version - 显示版本号")
	fmt.Println("  -c <file>      - 指定配置文件（默认 goclaw.yaml）")
	fmt.Printf("\n%sCLI 命令:%s\n", colorYellow, colorReset)
	fmt.Println("  /help    - 显示帮助")
	fmt.Println("  /memory  - 查看记忆状态")
	fmt.Println("  /clear   - 清空对话历史")
	fmt.Println("  /quit    - 退出")
	fmt.Printf("\n%s配置:%s\n", colorYellow, colorReset)
	fmt.Println("  goclaw.yaml — 主配置文件（参考 goclaw.example.yaml）")
	fmt.Println("  .env        — 环境变量（向后兼容，YAML 优先）")
	fmt.Printf("\n%s环境变量（YAML 为空时回退）:%s\n", colorYellow, colorReset)
	fmt.Println("  GOCLAW_PROVIDER      - 提供方 (claude/mimo/minimax/siliconflow/openai)")
	fmt.Println("  ANTHROPIC_API_KEY    - Claude API Key")
	fmt.Println("  MINIMAX_API_KEY      - MiniMax API Key")
	fmt.Println("  OPENAI_API_KEY       - OpenAI 兼容 API Key")
	fmt.Println("  OPENAI_BASE_URL      - API 地址 (DeepSeek/豆包/Ollama)")
	fmt.Println("  GOCLAW_MODEL         - 模型名称")
	fmt.Println("  TAVILY_API_KEY       - Tavily 搜索 API Key")
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

// connectMCPServersFromConfig 从 YAML 配置加载 MCP Servers
func connectMCPServersFromConfig(cfg *config.Config, registry *tools.Registry) *tools.MCPBridge {
	if len(cfg.MCP) == 0 {
		return nil
	}

	bridge := tools.NewMCPBridge()
	ctx := context.Background()

	for name, srv := range cfg.MCP {
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