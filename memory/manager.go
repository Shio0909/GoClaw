package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LLMCaller 用于调用 LLM 的接口（由 agent 层注入）
type LLMCaller func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// Manager 记忆管理器
type Manager struct {
	store     *Store
	rootStore *Store // 非 nil 时从 rootStore 读 soul（scoped 模式）
	llmCall   LLMCaller
	turnCount int
	refineAt  int // 每 N 轮触发一次记忆精炼
}

// NewManager 创建记忆管理器
func NewManager(store *Store, refineAt int) *Manager {
	if refineAt <= 0 {
		refineAt = 10
	}
	return &Manager{
		store:    store,
		refineAt: refineAt,
	}
}

// NewScopedManager 创建带作用域的记忆管理器
// rootStore 用于读取 soul.md（全局人格），userStore 用于读写 user.md / memory.md / logs
func NewScopedManager(rootStore, userStore *Store, refineAt int) *Manager {
	if refineAt <= 0 {
		refineAt = 10
	}
	return &Manager{
		store:     userStore,
		rootStore: rootStore,
		refineAt:  refineAt,
	}
}

// SetLLMCaller 设置 LLM 调用函数
func (m *Manager) SetLLMCaller(fn LLMCaller) {
	m.llmCall = fn
}

// Store 返回底层 Store
func (m *Manager) Store() *Store {
	return m.store
}

// BuildContext 构建记忆上下文（注入到 system prompt）
func (m *Manager) BuildContext() (string, error) {
	// scoped 模式：soul 从 rootStore 读，user/memory 从 store（userStore）读
	soulStore := m.store
	if m.rootStore != nil {
		soulStore = m.rootStore
	}
	soul, err := soulStore.ReadSoul()
	if err != nil {
		return "", fmt.Errorf("read soul: %w", err)
	}
	user, err := m.store.ReadUser()
	if err != nil {
		return "", fmt.Errorf("read user: %w", err)
	}
	mem, err := m.store.ReadMemory()
	if err != nil {
		return "", fmt.Errorf("read memory: %w", err)
	}

	var sb strings.Builder
	if soul != "" {
		sb.WriteString("=== SOUL (你的人格) ===\n")
		sb.WriteString(soul)
		sb.WriteString("\n\n")
	}
	if user != "" {
		sb.WriteString("=== USER (用户画像) ===\n")
		sb.WriteString(user)
		sb.WriteString("\n\n")
	}
	if mem != "" {
		sb.WriteString("=== MEMORY (长期记忆) ===\n")
		sb.WriteString(mem)
		sb.WriteString("\n\n")
	}
	return sb.String(), nil
}

// OnTurn 每轮对话后调用，记录日志并判断是否需要精炼
func (m *Manager) OnTurn(ctx context.Context, role, content string) {
	// 记录日志
	_ = m.store.AppendLog(LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      role,
		Content:   content,
	})

	m.turnCount++
	if m.turnCount%m.refineAt == 0 && m.llmCall != nil {
		go m.refine(ctx)
	}
}

// Refine 手动触发记忆精炼
func (m *Manager) Refine(ctx context.Context) error {
	if m.llmCall == nil {
		return fmt.Errorf("LLM caller not set")
	}
	m.refine(ctx)
	return nil
}

// refine 记忆精炼：让 LLM 从日志中提炼重要信息
func (m *Manager) refine(ctx context.Context) {
	logs, err := m.store.ReadTodayLogs()
	if err != nil || len(logs) == 0 {
		return
	}

	var logText strings.Builder
	for _, l := range logs {
		logText.WriteString(fmt.Sprintf("[%s] %s: %s\n", l.Timestamp, l.Role, l.Content))
	}

	currentMem, _ := m.store.ReadMemory()
	currentUser, _ := m.store.ReadUser()

	prompt := fmt.Sprintf(`以下是今天的对话日志：
%s

当前 memory.md 内容：
%s

当前 user.md 内容：
%s

请分析对话日志，提炼出值得长期记住的信息。
输出格式：
---MEMORY---
(更新后的 memory.md 完整内容)
---USER---
(更新后的 user.md 完整内容)`, logText.String(), currentMem, currentUser)

	result, err := m.llmCall(ctx, "你是一个记忆管理助手，负责从对话日志中提炼重要信息。", prompt)
	if err != nil {
		return
	}

	// 解析输出
	if memStart := strings.Index(result, "---MEMORY---"); memStart != -1 {
		if userStart := strings.Index(result, "---USER---"); userStart != -1 {
			memContent := strings.TrimSpace(result[memStart+len("---MEMORY---") : userStart])
			userContent := strings.TrimSpace(result[userStart+len("---USER---"):])
			if memContent != "" {
				_ = m.store.WriteMemory(memContent)
			}
			if userContent != "" {
				_ = m.store.WriteUser(userContent)
			}
		}
	}
}
