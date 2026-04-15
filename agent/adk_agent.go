package agent

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

// ADKAgent 基于 Eino ADK (adk.ChatModelAgent + adk.Runner) 的增强 Agent。
// 支持中断/恢复（interrupt/resume）、检查点持久化等高级特性。
type ADKAgent struct {
	cfg      Config
	registry *tools.Registry
	memMgr   *memory.Manager
	store    adk.CheckPointStore
	runner   *adk.Runner
	agent    *adk.ChatModelAgent
	history  []*schema.Message
	mu       sync.Mutex

	lastInterrupt *adkInterruptState
}

type adkInterruptState struct {
	CheckpointID string
	Info         string
	Contexts     []*adk.InterruptCtx
}

// ADKAgentConfig ADK Agent 配置
type ADKAgentConfig struct {
	Config                              // 基础 Agent 配置
	Name         string                 // Agent 名称（必填）
	Description  string                 // Agent 描述（必填）
	Instruction  string                 // System prompt
	Store        adk.CheckPointStore    // 检查点存储（nil=使用内存存储）
	Streaming    bool                   // 是否启用流式
	MaxIter      int                    // 最大迭代次数（0=默认 20）
}

// NewADKAgent 创建基于 ADK 的 Agent
func NewADKAgent(ctx context.Context, cfg ADKAgentConfig, registry *tools.Registry, memMgr *memory.Manager) (*ADKAgent, error) {
	if cfg.Name == "" {
		cfg.Name = "goclaw-agent"
	}
	if cfg.Description == "" {
		cfg.Description = "GoClaw AI Agent with checkpoint/resume support"
	}

	a := &ADKAgent{
		cfg:      cfg.Config,
		registry: registry,
		memMgr:   memMgr,
		store:    cfg.Store,
	}

	if a.store == nil {
		a.store = NewMemoryCheckPointStore()
	}

	// 创建模型（复用 Agent 的 createModel 逻辑）
	baseAgent := &Agent{cfg: cfg.Config, registry: registry, memMgr: memMgr}
	chatModel, err := baseAgent.createModel(ctx)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}

	// 组装 tools
	einoTools := registry.ToEinoTools()
	baseTools := make([]tool.BaseTool, len(einoTools))
	for i, t := range einoTools {
		baseTools[i] = t
	}

	// 构建 system prompt
	instruction := cfg.Instruction
	if instruction == "" {
		sp, err := BuildSystemPrompt(memMgr, registry)
		if err != nil {
			log.Printf("[ADK] 构建 system prompt 失败: %v", err)
			instruction = "你是一个有用的 AI 助手。"
		} else {
			instruction = sp
		}
	}

	maxIter := cfg.MaxIter
	if maxIter <= 0 {
		maxIter = 20
	}

	// 创建 ChatModelAgent
	cmAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        cfg.Name,
		Description: cfg.Description,
		Instruction: instruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: baseTools,
			},
		},
		MaxIterations: maxIter,
	})
	if err != nil {
		return nil, fmt.Errorf("create ChatModelAgent: %w", err)
	}
	a.agent = cmAgent

	// 创建 Runner
	a.runner = adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           cmAgent,
		EnableStreaming:  cfg.Streaming,
		CheckPointStore: a.store,
	})

	return a, nil
}

// Run 执行对话，返回 AgentEvent 迭代器
func (a *ADKAgent) Run(ctx context.Context, userInput string, checkpointID string) ([]*adk.AgentEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	msgs := make([]adk.Message, 0, len(a.history)+1)
	msgs = append(msgs, a.history...)
	msgs = append(msgs, schema.UserMessage(userInput))

	var opts []adk.AgentRunOption
	if checkpointID != "" {
		opts = append(opts, adk.WithCheckPointID(checkpointID))
	}

	iter := a.runner.Run(ctx, msgs, opts...)

	var events []*adk.AgentEvent
	var lastOutput *schema.Message
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return events, event.Err
		}
		events = append(events, event)

		// 检测中断
		if event.Action != nil && event.Action.Interrupted != nil {
			a.lastInterrupt = &adkInterruptState{
				CheckpointID: checkpointID,
				Info:         fmt.Sprintf("%v", event.Action.Interrupted.Data),
				Contexts:     event.Action.Interrupted.InterruptContexts,
			}
		}

		// 收集输出
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, err := event.Output.MessageOutput.GetMessage()
			if err == nil && msg != nil && msg.Role == schema.Assistant {
				lastOutput = msg
			}
		}
	}

	// 更新历史
	a.history = append(a.history, schema.UserMessage(userInput))
	if lastOutput != nil {
		a.history = append(a.history, lastOutput)
		if a.memMgr != nil {
			a.memMgr.OnTurn(ctx, "user", userInput)
			a.memMgr.OnTurn(ctx, "assistant", lastOutput.Content)
		}
	}

	return events, nil
}

// Resume 从中断点恢复执行
func (a *ADKAgent) Resume(ctx context.Context, checkpointID string, targets map[string]any) ([]*adk.AgentEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var (
		iter *adk.AsyncIterator[*adk.AgentEvent]
		err  error
	)

	if len(targets) > 0 {
		iter, err = a.runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
			Targets: targets,
		})
	} else {
		iter, err = a.runner.Resume(ctx, checkpointID)
	}
	if err != nil {
		return nil, fmt.Errorf("resume from checkpoint %s: %w", checkpointID, err)
	}

	var events []*adk.AgentEvent
	var lastOutput *schema.Message
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return events, event.Err
		}
		events = append(events, event)

		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, e := event.Output.MessageOutput.GetMessage()
			if e == nil && msg != nil && msg.Role == schema.Assistant {
				lastOutput = msg
			}
		}
	}

	if lastOutput != nil {
		a.history = append(a.history, lastOutput)
		if a.memMgr != nil {
			a.memMgr.OnTurn(ctx, "assistant", lastOutput.Content)
		}
	}

	a.lastInterrupt = nil
	return events, nil
}

// GetLastInterrupt 获取最近一次中断状态
func (a *ADKAgent) GetLastInterrupt() *adkInterruptState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastInterrupt
}

// GetHistory 获取对话历史
func (a *ADKAgent) GetHistory() []*schema.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]*schema.Message, len(a.history))
	copy(cp, a.history)
	return cp
}

// SetHistory 设置对话历史
func (a *ADKAgent) SetHistory(msgs []*schema.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = msgs
}

// ClearHistory 清空对话历史
func (a *ADKAgent) ClearHistory() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.history = nil
	a.lastInterrupt = nil
}
