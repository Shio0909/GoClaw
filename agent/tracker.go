package agent

import (
	"sync"
	"time"
)

// ToolCallRecord 单次工具调用记录
type ToolCallRecord struct {
	ToolName  string                 `json:"tool_name"`
	Args      map[string]interface{} `json:"args,omitempty"`
	Result    string                 `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Duration  time.Duration          `json:"duration_ms"`
	Timestamp time.Time              `json:"timestamp"`
	Success   bool                   `json:"success"`
}

// TurnRecord 一轮对话的完整记录
type TurnRecord struct {
	TurnID       int              `json:"turn_id"`
	UserInput    string           `json:"user_input"`
	Response     string           `json:"response,omitempty"`
	ToolCalls    []ToolCallRecord `json:"tool_calls,omitempty"`
	StartTime    time.Time        `json:"start_time"`
	EndTime      time.Time        `json:"end_time,omitempty"`
	Duration     time.Duration    `json:"duration_ms,omitempty"`
	TokensUsed   int              `json:"tokens_used,omitempty"`
	ModelUsed    string           `json:"model_used,omitempty"`
	WasFallback  bool             `json:"was_fallback,omitempty"`
	ErrorMessage string           `json:"error,omitempty"`
}

// TurnTracker 追踪 Agent 每轮对话中的工具调用
type TurnTracker struct {
	mu      sync.RWMutex
	turns   []TurnRecord
	current *TurnRecord
	maxKeep int // 保留最近 N 轮记录
}

// NewTurnTracker 创建追踪器
func NewTurnTracker(maxKeep int) *TurnTracker {
	if maxKeep <= 0 {
		maxKeep = 100
	}
	return &TurnTracker{maxKeep: maxKeep}
}

// StartTurn 开始新一轮
func (t *TurnTracker) StartTurn(userInput, model string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = &TurnRecord{
		TurnID:    len(t.turns) + 1,
		UserInput: userInput,
		StartTime: time.Now(),
		ModelUsed: model,
	}
}

// RecordToolCall 记录一次工具调用
func (t *TurnTracker) RecordToolCall(name string, args map[string]interface{}, result string, err error, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return
	}
	record := ToolCallRecord{
		ToolName:  name,
		Args:      args,
		Duration:  duration,
		Timestamp: time.Now(),
		Success:   err == nil,
	}
	// 截断结果避免内存爆炸
	if len(result) > 500 {
		record.Result = result[:500] + "..."
	} else {
		record.Result = result
	}
	if err != nil {
		record.Error = err.Error()
	}
	t.current.ToolCalls = append(t.current.ToolCalls, record)
}

// EndTurn 结束当前轮
func (t *TurnTracker) EndTurn(response string, wasFallback bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return
	}
	t.current.EndTime = time.Now()
	t.current.Duration = t.current.EndTime.Sub(t.current.StartTime)
	t.current.WasFallback = wasFallback
	if len(response) > 1000 {
		t.current.Response = response[:1000] + "..."
	} else {
		t.current.Response = response
	}
	if err != nil {
		t.current.ErrorMessage = err.Error()
	}
	t.turns = append(t.turns, *t.current)
	// 淘汰旧记录
	if len(t.turns) > t.maxKeep {
		t.turns = t.turns[len(t.turns)-t.maxKeep:]
	}
	t.current = nil
}

// GetTurns 获取所有记录
func (t *TurnTracker) GetTurns() []TurnRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cp := make([]TurnRecord, len(t.turns))
	copy(cp, t.turns)
	return cp
}

// GetRecentTurns 获取最近 N 轮
func (t *TurnTracker) GetRecentTurns(n int) []TurnRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n >= len(t.turns) {
		cp := make([]TurnRecord, len(t.turns))
		copy(cp, t.turns)
		return cp
	}
	cp := make([]TurnRecord, n)
	copy(cp, t.turns[len(t.turns)-n:])
	return cp
}

// Summary 汇总统计
func (t *TurnTracker) Summary() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	totalTools := 0
	totalErrors := 0
	var totalDuration time.Duration
	toolFreq := map[string]int{}

	for _, turn := range t.turns {
		totalTools += len(turn.ToolCalls)
		totalDuration += turn.Duration
		for _, tc := range turn.ToolCalls {
			toolFreq[tc.ToolName]++
			if !tc.Success {
				totalErrors++
			}
		}
	}

	// 找最常用工具
	topTool := ""
	topCount := 0
	for name, count := range toolFreq {
		if count > topCount {
			topTool = name
			topCount = count
		}
	}

	avgDuration := time.Duration(0)
	if len(t.turns) > 0 {
		avgDuration = totalDuration / time.Duration(len(t.turns))
	}

	return map[string]interface{}{
		"total_turns":          len(t.turns),
		"total_tool_calls":     totalTools,
		"total_tool_errors":    totalErrors,
		"avg_turn_duration_ms": avgDuration.Milliseconds(),
		"top_tool":             topTool,
		"top_tool_calls":       topCount,
		"unique_tools_used":    len(toolFreq),
		"tool_frequency":       toolFreq,
	}
}
