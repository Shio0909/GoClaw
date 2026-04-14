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
	"github.com/goclaw/goclaw/rag"
	"github.com/goclaw/goclaw/tools"
)

// loggingTransport 记录 HTTP 请求大小
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
	Provider        string   // "openai", "claude", "mimo", ...
	APIKey          string
	BaseURL         string
	Model           string
	MaxStep         int      // 最大工具调用步数（0 = 默认 25）
	Temperature     *float32 // 采样温度（nil = 使用默认值）
	MaxTokens       int      // 最大输出 token（0 = 使用默认值）
	ReasoningEffort string   // 推理力度: low, medium, high（仅推理模型）
}

// Agent 核心 Agent，基于 Eino react agent
type Agent struct {
	cfg               Config
	registry          *tools.Registry
	memMgr            *memory.Manager
	ragMgr            *rag.Manager   // RAG 检索增强（可选）
	history           []*schema.Message
	extraSystemPrompt string       // 额外的 system prompt（如 QQ 聊天模式指令）
	compressor        *Compressor  // 上下文压缩器（可选）
	retryConfig       *RetryConfig // 重试 + Key 轮换配置（可选）
	router            *ModelRouter // 智能模型路由器（可选）
	skillLearner      *SkillLearner // 技能自学习引擎（可选）
	maxStep           int          // 最大工具调用步数
}

// NewAgent 创建 Agent
func NewAgent(cfg Config, registry *tools.Registry, memMgr *memory.Manager) *Agent {
	maxStep := 25
	if cfg.MaxStep > 0 {
		maxStep = cfg.MaxStep
	}
	return &Agent{
		cfg:      cfg,
		registry: registry,
		memMgr:   memMgr,
		maxStep:  maxStep,
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

// SetRouter 设置智能模型路由器
func (a *Agent) SetRouter(r *ModelRouter) {
	a.router = r
}

// SetRAGManager 设置 RAG 检索增强管理器
func (a *Agent) SetRAGManager(m *rag.Manager) {
	a.ragMgr = m
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
		maxTokens := 4096
		if a.cfg.MaxTokens > 0 {
			maxTokens = a.cfg.MaxTokens
		}
		return claudemodel.NewChatModel(ctx, &claudemodel.Config{
			BaseURL:    baseURLPtr,
			APIKey:     a.cfg.APIKey,
			Model:      a.cfg.Model,
			MaxTokens:  maxTokens,
			HTTPClient: &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		})
	default: // openai 兼容
		cfg := &openaimodel.ChatModelConfig{
			APIKey:      a.cfg.APIKey,
			BaseURL:     a.cfg.BaseURL,
			Model:       a.cfg.Model,
			Temperature: a.cfg.Temperature,
			HTTPClient:  &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		}
		if a.cfg.MaxTokens > 0 {
			cfg.MaxCompletionTokens = &a.cfg.MaxTokens
		}
		if a.cfg.ReasoningEffort != "" {
			cfg.ReasoningEffort = openaimodel.ReasoningEffortLevel(a.cfg.ReasoningEffort)
		}
		return openaimodel.NewChatModel(ctx, cfg)
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
		MaxStep: a.maxStep,
		// 默认 firstChunkStreamToolCallChecker 只检查首个非空 chunk，
		// 对于有 <think> 推理的模型（MiniMax、DeepSeek 等）会先输出
		// content 再输出 tool_calls，导致工具调用被忽略。
		// 这里用 allChunksToolCallChecker 遍历全部 chunk。
		StreamToolCallChecker: allChunksToolCallChecker,
	})
}

// allChunksToolCallChecker 遍历所有 stream chunk 检测 tool_calls。
// 解决 "thinking" 模型先输出 <think> 内容、后输出 tool_calls 的兼容问题。
func allChunksToolCallChecker(_ context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
	defer sr.Close()
	for {
		msg, err := sr.Recv()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(msg.ToolCalls) > 0 {
			return true, nil
		}
	}
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

	// RAG context injection (query-time retrieval)
	if a.ragMgr != nil && a.ragMgr.HasProviders() {
		ragCtx := a.ragMgr.BuildContext(ctx, userInput)
		if ragCtx != "" {
			systemPrompt += "\n" + ragCtx
		}
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
		a.applyRoute(userInput)
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

	content := StripThinkTags(resp.Content)
	cleanResp := schema.AssistantMessage(content, nil)
	a.history = append(a.history, schema.UserMessage(userInput), cleanResp)
	a.memMgr.OnTurn(ctx, "assistant", content)
	a.CheckSkillLearning(ctx, content)
	return content, nil
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
		a.applyRoute(userInput)
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
	a.CheckSkillLearning(ctx, content)
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
		a.applyRoute(text)
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

	content := StripThinkTags(resp.Content)
	cleanResp := schema.AssistantMessage(content, nil)
	a.history = append(a.history, schema.UserMessage(text), cleanResp)
	a.memMgr.OnTurn(ctx, "assistant", content)
	a.CheckSkillLearning(ctx, content)
	return content, nil
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

// applyRoute 应用模型路由（如果配置了路由器）
func (a *Agent) applyRoute(input string) {
	if a.router == nil {
		return
	}
	route := a.router.Route(input)
	if route.Model != "" {
		a.cfg.Model = route.Model
	}
	if route.Provider != "" {
		a.cfg.Provider = route.Provider
	}
	if route.BaseURL != "" {
		a.cfg.BaseURL = route.BaseURL
	}
}

// MemoryManager 返回记忆管理器
func (a *Agent) MemoryManager() *memory.Manager {
	return a.memMgr
}
