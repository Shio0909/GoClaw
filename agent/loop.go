package agent

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

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

// loggingTransport 记录 HTTP 请求体大小
type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		log.Printf("[HTTP] %s %s — 请求体 %d 字节 (%.1f KB)", req.Method, req.URL.Path, len(body), float64(len(body))/1024)
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		req.ContentLength = int64(len(body))
	}
	return t.base.RoundTrip(req)
}

// Config Agent 配置
type Config struct {
	Provider string // "openai", "claude", "mimo", ...
	APIKey   string
	BaseURL  string
	Model    string
}

// Agent 核心 Agent，基于 Eino react agent
type Agent struct {
	cfg               Config
	registry          *tools.Registry
	memMgr            *memory.Manager
	history           []*schema.Message
	extraSystemPrompt string       // 额外的 system prompt（如 QQ 聊天模式指令）
	compressor        *Compressor  // 上下文压缩器（可选）
	retryConfig       *RetryConfig // 重试 + Key 轮换配置（可选）
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

// SetMemoryManager 替换记忆管理器（用于群聊中按用户切换记忆）
func (a *Agent) SetMemoryManager(mgr *memory.Manager) {
	a.memMgr = mgr
}

// SetCompressor 设置上下文压缩器
func (a *Agent) SetCompressor(c *Compressor) {
	a.compressor = c
}

// SetRetryConfig 设置重试 + Key 轮换配置
func (a *Agent) SetRetryConfig(cfg *RetryConfig) {
	a.retryConfig = cfg
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
			BaseURL:    baseURLPtr,
			APIKey:     a.cfg.APIKey,
			Model:      a.cfg.Model,
			MaxTokens:  4096,
			HTTPClient: &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		})
	default: // openai 兼容
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:     a.cfg.APIKey,
			BaseURL:    a.cfg.BaseURL,
			Model:      a.cfg.Model,
			HTTPClient: &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
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

	// 压缩上下文（如果已配置压缩器）
	history := a.history
	if a.compressor != nil {
		history = a.compressor.CompressIfNeeded(ctx, history)
		if len(history) != len(a.history) {
			a.history = history // 压缩后更新历史
		}
	}

	msgs = append(msgs, history...)
	msgs = append(msgs, schema.UserMessage(userInput))
	return msgs, nil
}

// Run 执行一轮对话（非流式，用于记忆系统等内部调用）
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	msgs, err := a.buildMessages(ctx, userInput)
	if err != nil {
		return "", err
	}

	a.memMgr.OnTurn(ctx, "user", userInput)

	var resp *schema.Message
	runFn := func(cfg Config) error {
		origKey := a.cfg.APIKey
		a.cfg.APIKey = cfg.APIKey
		defer func() { a.cfg.APIKey = origKey }()

		agent, err := a.buildReactAgent(ctx)
		if err != nil {
			return err
		}
		r, err := agent.Generate(ctx, msgs)
		if err != nil {
			return err
		}
		resp = r
		return nil
	}

	if a.retryConfig != nil {
		err = a.runWithRetry(ctx, runFn)
	} else {
		err = runFn(a.cfg)
	}
	if err != nil {
		return "", fmt.Errorf("agent generate: %w", err)
	}

	a.history = append(a.history, schema.UserMessage(userInput), resp)
	a.memMgr.OnTurn(ctx, "assistant", resp.Content)
	return resp.Content, nil
}

// RunStream 执行一轮对话（流式输出）
func (a *Agent) RunStream(ctx context.Context, userInput string) (*schema.StreamReader[*schema.Message], error) {
	msgs, err := a.buildMessages(ctx, userInput)
	if err != nil {
		return nil, err
	}

	a.memMgr.OnTurn(ctx, "user", userInput)
	a.history = append(a.history, schema.UserMessage(userInput))

	var stream *schema.StreamReader[*schema.Message]
	runFn := func(cfg Config) error {
		origKey := a.cfg.APIKey
		a.cfg.APIKey = cfg.APIKey
		defer func() { a.cfg.APIKey = origKey }()

		agent, err := a.buildReactAgent(ctx)
		if err != nil {
			return err
		}
		s, err := agent.Stream(ctx, msgs)
		if err != nil {
			return err
		}
		stream = s
		return nil
	}

	if a.retryConfig != nil {
		err = a.runWithRetry(ctx, runFn)
	} else {
		err = runFn(a.cfg)
	}
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

// ImageInput 图片输入（base64 编码）
type ImageInput struct {
	Base64Data string
	MIMEType   string
}

// RunWithImages 带图片的对话（非流式）
func (a *Agent) RunWithImages(ctx context.Context, text string, images []ImageInput) (string, error) {
	msgs, err := a.buildMultimodalMessages(ctx, text, images)
	if err != nil {
		return "", err
	}

	a.memMgr.OnTurn(ctx, "user", text)

	var resp *schema.Message
	runFn := func(cfg Config) error {
		origKey := a.cfg.APIKey
		a.cfg.APIKey = cfg.APIKey
		defer func() { a.cfg.APIKey = origKey }()

		agent, err := a.buildReactAgent(ctx)
		if err != nil {
			return err
		}
		r, err := agent.Generate(ctx, msgs)
		if err != nil {
			return err
		}
		resp = r
		return nil
	}

	if a.retryConfig != nil {
		err = a.runWithRetry(ctx, runFn)
	} else {
		err = runFn(a.cfg)
	}
	if err != nil {
		return "", fmt.Errorf("agent generate: %w", err)
	}

	a.history = append(a.history, schema.UserMessage(text), resp)
	a.memMgr.OnTurn(ctx, "assistant", resp.Content)
	return resp.Content, nil
}

// buildMultimodalMessages 构建包含图片的消息列表
func (a *Agent) buildMultimodalMessages(ctx context.Context, text string, images []ImageInput) ([]*schema.Message, error) {
	systemPrompt, err := BuildSystemPrompt(a.memMgr, a.registry)
	if err != nil {
		return nil, fmt.Errorf("build system prompt: %w", err)
	}
	if a.extraSystemPrompt != "" {
		systemPrompt += "\n\n" + a.extraSystemPrompt
	}

	// 构建多模态 content parts
	parts := []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: text},
	}
	for _, img := range images {
		b64 := img.Base64Data
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &b64,
					MIMEType:   img.MIMEType,
				},
			},
		})
	}

	userMsg := &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	}

	msgs := []*schema.Message{schema.SystemMessage(systemPrompt)}
	msgs = append(msgs, a.history...)
	msgs = append(msgs, userMsg)
	return msgs, nil
}

// ClearHistory 清空对话历史
func (a *Agent) ClearHistory() {
	a.history = nil
}

// MemoryManager 返回记忆管理器
func (a *Agent) MemoryManager() *memory.Manager {
	return a.memMgr
}
