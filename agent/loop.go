package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// Config Agent 配置
type Config struct {
	Provider string // "openai" 或 "claude"
	APIKey   string
	BaseURL  string
	Model    string
}

// Agent 核心 Agent，基于 Eino react agent
type Agent struct {
	cfg              Config
	registry         *tools.Registry
	memMgr           *memory.Manager
	history          []*schema.Message
	extraSystemPrompt string // 额外的 system prompt（如 QQ 聊天模式指令）
}

// NewAgent 创建 Agent
func NewAgent(cfg Config, registry *tools.Registry, memMgr *memory.Manager) *Agent {
	return &Agent{
		cfg:      cfg,
		registry: registry,
		memMgr:   memMgr,
	}
}

// SetExtraSystemPrompt 设置额外的 system prompt（会追加到默认 prompt 之后）
func (a *Agent) SetExtraSystemPrompt(prompt string) {
	a.extraSystemPrompt = prompt
}

// createModel 根据 provider 配置创建 Eino 模型
func (a *Agent) createModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	switch a.cfg.Provider {
	case "claude":
		baseURL := a.cfg.BaseURL
		var baseURLPtr *string
		if baseURL != "" {
			baseURLPtr = &baseURL
		}
		return claudemodel.NewChatModel(ctx, &claudemodel.Config{
			BaseURL:   baseURLPtr,
			APIKey:    a.cfg.APIKey,
			Model:     a.cfg.Model,
			MaxTokens: 4096,
		})
	default: // openai 兼容
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:  a.cfg.APIKey,
			BaseURL: a.cfg.BaseURL,
			Model:   a.cfg.Model,
		})
	}
}

// buildReactAgent 创建 Eino react agent
func (a *Agent) buildReactAgent(ctx context.Context) (*react.Agent, error) {
	chatModel, err := a.createModel(ctx)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}

	einoTools := a.registry.ToEinoTools()
	baseTools := make([]tool.BaseTool, len(einoTools))
	for i, t := range einoTools {
		baseTools[i] = t
	}

	return react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: baseTools,
		},
		MaxStep: 10,
	})
}

// buildMessages 构建发送给 Eino agent 的消息列表
func (a *Agent) buildMessages(ctx context.Context, userInput string) ([]*schema.Message, error) {
	systemPrompt, err := BuildSystemPrompt(a.memMgr, a.registry)
	if err != nil {
		return nil, fmt.Errorf("build system prompt: %w", err)
	}
	if a.extraSystemPrompt != "" {
		systemPrompt += "\n\n" + a.extraSystemPrompt
	}

	msgs := []*schema.Message{schema.SystemMessage(systemPrompt)}
	msgs = append(msgs, a.history...)
	msgs = append(msgs, schema.UserMessage(userInput))
	return msgs, nil
}

// Run 执行一轮对话（非流式，用于记忆系统等内部调用）
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	agent, err := a.buildReactAgent(ctx)
	if err != nil {
		return "", err
	}

	msgs, err := a.buildMessages(ctx, userInput)
	if err != nil {
		return "", err
	}

	a.memMgr.OnTurn(ctx, "user", userInput)

	resp, err := agent.Generate(ctx, msgs)
	if err != nil {
		return "", fmt.Errorf("agent generate: %w", err)
	}

	a.history = append(a.history, schema.UserMessage(userInput), resp)
	a.memMgr.OnTurn(ctx, "assistant", resp.Content)
	return resp.Content, nil
}

// RunStream 执行一轮对话（流式输出）
func (a *Agent) RunStream(ctx context.Context, userInput string) (*schema.StreamReader[*schema.Message], error) {
	agent, err := a.buildReactAgent(ctx)
	if err != nil {
		return nil, err
	}

	msgs, err := a.buildMessages(ctx, userInput)
	if err != nil {
		return nil, err
	}

	a.memMgr.OnTurn(ctx, "user", userInput)
	a.history = append(a.history, schema.UserMessage(userInput))

	stream, err := agent.Stream(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("agent stream: %w", err)
	}
	return stream, nil
}

// AppendAssistantMessage 流式输出完成后，将完整回复加入历史
func (a *Agent) AppendAssistantMessage(ctx context.Context, content string) {
	a.history = append(a.history, schema.AssistantMessage(content, nil))
	a.memMgr.OnTurn(ctx, "assistant", content)
}

// ClearHistory 清空对话历史
func (a *Agent) ClearHistory() {
	a.history = nil
}
