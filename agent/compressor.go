package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/goclaw/goclaw/memory"
)

// 压缩算法常量（借鉴 Hermes ContextCompressor）
const (
	defaultContextLength   = 128000 // 默认上下文窗口大小（token）
	defaultThresholdPct    = 0.50   // 超过 50% 触发压缩
	defaultProtectFirstN   = 2      // 保护头部 N 条消息（system 不计入）
	defaultTailTokenBudget = 20000  // 尾部保护 token 预算
	minSummaryTokens       = 2000
	summaryTokensCeiling   = 12000
	summaryRatio           = 0.20 // 摘要 token 占被压缩内容的 20%
	oldToolOutputThreshold = 200  // 超过此字符数的旧工具输出将被裁剪
	summaryCooldown        = 10 * time.Minute
)

// Compressor 上下文压缩器，三阶段有损压缩
type Compressor struct {
	contextLength   int
	thresholdPct    float64
	protectFirstN   int
	tailTokenBudget int
	llmCall         memory.LLMCaller

	mu              sync.Mutex
	previousSummary string    // 上一次的结构化摘要（迭代更新）
	lastFailure     time.Time // 摘要失败冷却
}

// CompressorConfig 压缩器配置
type CompressorConfig struct {
	ContextLength   int     // 模型上下文窗口大小（token）
	ThresholdPct    float64 // 触发压缩的阈值比例（0.0-1.0）
	ProtectFirstN   int     // 保护头部消息数量
	TailTokenBudget int     // 尾部保护 token 预算
}

// NewCompressor 创建压缩器
func NewCompressor(cfg CompressorConfig, llmCall memory.LLMCaller) *Compressor {
	if cfg.ContextLength <= 0 {
		cfg.ContextLength = defaultContextLength
	}
	if cfg.ThresholdPct <= 0 {
		cfg.ThresholdPct = defaultThresholdPct
	}
	if cfg.ProtectFirstN <= 0 {
		cfg.ProtectFirstN = defaultProtectFirstN
	}
	if cfg.TailTokenBudget <= 0 {
		cfg.TailTokenBudget = defaultTailTokenBudget
	}
	return &Compressor{
		contextLength:   cfg.ContextLength,
		thresholdPct:    cfg.ThresholdPct,
		protectFirstN:   cfg.ProtectFirstN,
		tailTokenBudget: cfg.TailTokenBudget,
		llmCall:         llmCall,
	}
}

// estimateTokens 粗略估算消息列表的 token 数（CJK 感知）
// CJK 字符约 1-2 字符 = 1 token，ASCII 约 4 字符 = 1 token
func estimateTokens(msgs []*schema.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateStringTokens(m.Content) + 4 // 每条消息 +4 token 开销
		for _, tc := range m.ToolCalls {
			total += estimateStringTokens(tc.Function.Arguments)
			total += estimateStringTokens(tc.Function.Name) + 4
		}
	}
	return total
}

// estimateStringTokens 估算单个字符串的 token 数
func estimateStringTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	tokens := 0
	asciiRun := 0
	for _, r := range s {
		if r >= 0x3000 || (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xAC00 && r <= 0xD7AF) {
			// CJK Unified Ideographs, CJK symbols, Korean — ~1.5 chars per token
			if asciiRun > 0 {
				tokens += (asciiRun + 3) / 4
				asciiRun = 0
			}
			tokens++
		} else {
			asciiRun++
		}
	}
	if asciiRun > 0 {
		tokens += (asciiRun + 3) / 4
	}
	return tokens
}

// ShouldCompress 检查是否需要压缩
func (c *Compressor) ShouldCompress(msgs []*schema.Message) bool {
	tokens := estimateTokens(msgs)
	threshold := int(float64(c.contextLength) * c.thresholdPct)
	return tokens > threshold
}

// CompressIfNeeded 如果超过阈值则执行压缩，返回压缩后的历史（不含 system 消息）
func (c *Compressor) CompressIfNeeded(ctx context.Context, history []*schema.Message) []*schema.Message {
	if !c.ShouldCompress(history) {
		return history
	}

	before := estimateTokens(history)
	log.Printf("[Compressor] 触发压缩：当前 %d tokens，阈值 %d tokens",
		before, int(float64(c.contextLength)*c.thresholdPct))

	result := c.compress(ctx, history)

	after := estimateTokens(result)
	log.Printf("[Compressor] 压缩完成：%d → %d tokens（节省 %.0f%%）",
		before, after, float64(before-after)/float64(before)*100)

	return result
}

// ForceCompress 强制执行压缩（忽略阈值检查，用于上下文溢出错误恢复）
func (c *Compressor) ForceCompress(ctx context.Context, history []*schema.Message) []*schema.Message {
	if len(history) < 3 {
		return history
	}
	before := estimateTokens(history)
	log.Printf("[Compressor] 强制压缩：当前 %d tokens", before)

	result := c.compress(ctx, history)

	after := estimateTokens(result)
	log.Printf("[Compressor] 强制压缩完成：%d → %d tokens（节省 %.0f%%）",
		before, after, float64(before-after)/float64(before)*100)
	return result
}

// compress 执行三阶段压缩
func (c *Compressor) compress(ctx context.Context, history []*schema.Message) []*schema.Message {
	// 深拷贝，避免修改原始消息
	msgs := deepCopyMessages(history)

	// Phase 1: 裁剪旧工具输出（免费操作）
	c.pruneOldToolResults(msgs)

	// 裁剪后如果已经低于阈值，直接返回
	if !c.ShouldCompress(msgs) {
		log.Printf("[Compressor] Phase 1 裁剪后已低于阈值，跳过摘要")
		return msgs
	}

	// Phase 2: 确定保护边界
	headEnd := c.protectFirstN
	if headEnd > len(msgs) {
		headEnd = len(msgs)
	}
	// 尾部边界：从末尾向前按 token 预算保护
	tailStart := c.findTailCutByTokens(msgs, headEnd)

	// 如果没有可压缩的中间部分，直接返回
	if tailStart <= headEnd {
		log.Printf("[Compressor] 无可压缩的中间部分")
		return msgs
	}

	// Phase 3: LLM 结构化摘要
	middleMsgs := msgs[headEnd:tailStart]
	summary := c.generateSummary(ctx, middleMsgs)

	// 组装压缩后的消息
	var result []*schema.Message
	result = append(result, msgs[:headEnd]...)
	if summary != "" {
		result = append(result, schema.SystemMessage(
			fmt.Sprintf("[上下文压缩摘要 — 以下内容是对之前对话的结构化总结]\n\n%s", summary)))
	}
	result = append(result, msgs[tailStart:]...)

	// 修复工具对完整性
	result = c.sanitizeToolPairs(result)

	return result
}

// pruneOldToolResults Phase 1: 裁剪旧工具输出
// 保留最后 5 条工具结果完整内容，其余长结果替换为占位符
func (c *Compressor) pruneOldToolResults(msgs []*schema.Message) {
	// 找到所有工具结果消息的索引
	var toolResultIdxs []int
	for i, m := range msgs {
		if m.Role == schema.Tool && len(m.Content) > oldToolOutputThreshold {
			toolResultIdxs = append(toolResultIdxs, i)
		}
	}

	// 保留最后 5 条
	if len(toolResultIdxs) <= 5 {
		return
	}
	toPrune := toolResultIdxs[:len(toolResultIdxs)-5]

	pruned := 0
	for _, idx := range toPrune {
		msgs[idx].Content = "[旧工具输出已清除以节省上下文空间]"
		pruned++
	}

	if pruned > 0 {
		log.Printf("[Compressor] Phase 1: 裁剪了 %d 条旧工具输出", pruned)
	}
}

// findTailCutByTokens 从消息末尾向前，按 token 预算确定尾部保护起点
// 返回 tailStart 索引（tailStart..len 为受保护的尾部）
func (c *Compressor) findTailCutByTokens(msgs []*schema.Message, headEnd int) int {
	budget := c.tailTokenBudget
	for i := len(msgs) - 1; i >= headEnd; i-- {
		msgTokens := estimateTokens(msgs[i : i+1])
		budget -= msgTokens
		if budget <= 0 {
			// 确保不在 assistant+tool_result 组中间断开
			return c.adjustBoundary(msgs, i+1, headEnd)
		}
	}
	return headEnd
}

// adjustBoundary 调整边界，确保不在 assistant(tool_call) + tool(result) 组中间断开
func (c *Compressor) adjustBoundary(msgs []*schema.Message, idx, headEnd int) int {
	if idx >= len(msgs) || idx <= headEnd {
		return idx
	}
	// 如果边界处是 tool 消息（孤立的 tool result），向前移到对应的 assistant 消息
	for idx < len(msgs) && msgs[idx].Role == schema.Tool {
		idx++
	}
	return idx
}

// generateSummary Phase 3: 使用 LLM 生成结构化摘要
func (c *Compressor) generateSummary(ctx context.Context, middleMsgs []*schema.Message) string {
	c.mu.Lock()
	if !c.lastFailure.IsZero() && time.Since(c.lastFailure) < summaryCooldown {
		c.mu.Unlock()
		log.Printf("[Compressor] 摘要生成冷却中，跳过")
		return c.previousSummary
	}
	prevSummary := c.previousSummary
	c.mu.Unlock()

	if c.llmCall == nil {
		log.Printf("[Compressor] LLM caller 未设置，跳过摘要")
		return prevSummary
	}

	// 估算摘要 token 预算
	middleTokens := estimateTokens(middleMsgs)
	summaryBudget := int(float64(middleTokens) * summaryRatio)
	if summaryBudget < minSummaryTokens {
		summaryBudget = minSummaryTokens
	}
	if summaryBudget > summaryTokensCeiling {
		summaryBudget = summaryTokensCeiling
	}

	// 构建对话文本
	var convText strings.Builder
	for _, m := range middleMsgs {
		role := string(m.Role)
		convText.WriteString(fmt.Sprintf("[%s]: %s\n", role, truncate(m.Content, 1000)))
		for _, tc := range m.ToolCalls {
			convText.WriteString(fmt.Sprintf("  → 工具调用: %s(%s)\n",
				tc.Function.Name, truncate(tc.Function.Arguments, 200)))
		}
	}

	systemPrompt := "你是一个对话摘要助手。请严格按照指定格式输出结构化摘要，保留关键信息。"

	var userPrompt string
	if prevSummary != "" {
		// 迭代更新模式
		userPrompt = fmt.Sprintf(`以下是之前的摘要：
---
%s
---

以下是新的对话内容（需要合并到摘要中）：
---
%s
---

请更新摘要，保留所有重要信息。摘要预算约 %d tokens。
输出格式：

## 目标
（用户的主要目标和意图）

## 进展
（已完成的关键步骤）

## 关键决策
（重要的技术决策和选择）

## 相关文件
（涉及的重要文件和路径）

## 下一步
（待完成的工作）

## 关键上下文
（不能丢失的重要信息，如 API key、配置值等）

## 工具与模式
（使用过的工具和工作模式）`, prevSummary, convText.String(), summaryBudget)
	} else {
		// 首次摘要
		userPrompt = fmt.Sprintf(`请总结以下对话内容。摘要预算约 %d tokens。

对话内容：
---
%s
---

输出格式：

## 目标
（用户的主要目标和意图）

## 进展
（已完成的关键步骤）

## 关键决策
（重要的技术决策和选择）

## 相关文件
（涉及的重要文件和路径）

## 下一步
（待完成的工作）

## 关键上下文
（不能丢失的重要信息）

## 工具与模式
（使用过的工具和工作模式）`, summaryBudget, convText.String())
	}

	result, err := c.llmCall(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Compressor] 摘要生成失败: %v", err)
		c.mu.Lock()
		c.lastFailure = time.Now()
		c.mu.Unlock()
		return prevSummary
	}

	c.mu.Lock()
	c.previousSummary = result
	c.lastFailure = time.Time{} // 清除冷却
	c.mu.Unlock()

	log.Printf("[Compressor] Phase 3: 摘要生成完成（%d 字符）", len(result))
	return result
}

// sanitizeToolPairs 修复压缩后的工具对完整性
func (c *Compressor) sanitizeToolPairs(msgs []*schema.Message) []*schema.Message {
	// 收集所有 tool_call ID 和 tool_result ID
	callIDs := make(map[string]bool)  // assistant 发出的 tool_call IDs
	resultIDs := make(map[string]bool) // tool result 对应的 tool_call IDs

	for _, m := range msgs {
		if m.Role == schema.Assistant {
			for _, tc := range m.ToolCalls {
				callIDs[tc.ID] = true
			}
		}
		if m.Role == schema.Tool && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}

	var result []*schema.Message
	for _, m := range msgs {
		// 移除孤立的 tool result（没有对应的 tool_call）
		if m.Role == schema.Tool && m.ToolCallID != "" && !callIDs[m.ToolCallID] {
			continue
		}
		result = append(result, m)
	}

	// 为孤立的 tool_call 插入 stub result
	var final []*schema.Message
	for _, m := range result {
		final = append(final, m)
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !resultIDs[tc.ID] {
					final = append(final, &schema.Message{
						Role:       schema.Tool,
						Content:    "[工具结果已在上下文压缩中省略]",
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					})
				}
			}
		}
	}

	return final
}

// deepCopyMessages 深拷贝消息列表
func deepCopyMessages(msgs []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, len(msgs))
	for i, m := range msgs {
		cp := *m
		if len(m.ToolCalls) > 0 {
			cp.ToolCalls = make([]schema.ToolCall, len(m.ToolCalls))
			copy(cp.ToolCalls, m.ToolCalls)
		}
		result[i] = &cp
	}
	return result
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
