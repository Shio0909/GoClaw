package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"

	"github.com/cloudwego/eino/schema"
)

// SkillLearnerConfig 技能自学习配置
type SkillLearnerConfig struct {
	// NudgeInterval 每多少轮对话后触发一次技能审查（默认 8）
	NudgeInterval int
	// SkillsDir 技能目录路径
	SkillsDir string
}

// SkillLearner 技能自学习引擎
// 跟踪对话轮次，在复杂任务完成后异步审查对话，自动创建或改进技能
type SkillLearner struct {
	cfg SkillLearnerConfig

	mu             sync.Mutex
	turnsSinceSkill int // 自上次技能操作以来的对话轮次
	agentCfg       Config
	registry       *tools.Registry
	memStore       *memory.Store
}

// NewSkillLearner 创建技能自学习引擎
func NewSkillLearner(cfg SkillLearnerConfig, agentCfg Config, registry *tools.Registry, memStore *memory.Store) *SkillLearner {
	if cfg.NudgeInterval <= 0 {
		cfg.NudgeInterval = 8
	}
	return &SkillLearner{
		cfg:      cfg,
		agentCfg: agentCfg,
		registry: registry,
		memStore: memStore,
	}
}

// OnTurn 记录一轮对话完成
// 如果包含技能操作（安装/更新/删除），重置计数器
func (l *SkillLearner) OnTurn(assistantReply string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 检测是否包含技能操作
	if containsSkillAction(assistantReply) {
		l.turnsSinceSkill = 0
		return
	}
	l.turnsSinceSkill++
}

// ShouldReview 检查是否应该触发技能审查
func (l *SkillLearner) ShouldReview() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turnsSinceSkill >= l.cfg.NudgeInterval
}

// ResetCounter 重置计数器（审查触发后调用）
func (l *SkillLearner) ResetCounter() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turnsSinceSkill = 0
}

// 技能审查提示词
const skillReviewPrompt = `回顾上面的对话历史，判断是否值得创建或更新一个技能。

请关注以下情况：
- 是否完成了一个复杂任务（经过多步尝试、纠错、或用户修正）
- 是否发现了一个可复用的工作流程或方法
- 是否有用户反复要求的特定任务模式
- 是否在使用已有技能时发现了遗漏或改进点

如果值得保存，请使用 skill_install 创建新技能，或 skill_update 改进已有技能。
技能应包含：触发条件、具体步骤、注意事项、输出格式。

如果没有值得保存的内容，直接说"没有需要保存的技能。"然后停止。`

// RunBackgroundReview 在后台审查对话历史，可能创建或更新技能
// 这个方法在独立 goroutine 中运行，不阻塞主线程
func (l *SkillLearner) RunBackgroundReview(ctx context.Context, conversationHistory []*schema.Message) {
	l.ResetCounter()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[SkillLearner] 后台审查 panic: %v", r)
			}
		}()

		log.Printf("[SkillLearner] 开始后台技能审查（对话 %d 条消息）...", len(conversationHistory))

		// 创建一个精简的临时 agent 用于审查
		// 只提供技能相关工具
		skillRegistry := tools.NewRegistry()
		skillRegistry.Register(tools.NewSkillInstallTool())
		tools.RegisterSkillTools(skillRegistry)

		memMgr := memory.NewManager(l.memStore, 999) // 不触发记忆提炼
		reviewAgent := NewAgent(l.agentCfg, skillRegistry, memMgr)

		// 将历史对话注入到审查 agent
		reviewAgent.history = make([]*schema.Message, len(conversationHistory))
		copy(reviewAgent.history, conversationHistory)

		// 如果有重试配置，也设上
		if l.agentCfg.APIKey != "" {
			reviewAgent.SetRetryConfig(&RetryConfig{
				MaxAttempts: 2,
			})
		}

		// 执行审查
		reply, err := reviewAgent.Run(ctx, skillReviewPrompt)
		if err != nil {
			log.Printf("[SkillLearner] 后台审查失败: %v", err)
			return
		}

		reply = strings.TrimSpace(reply)
		if reply == "" || strings.Contains(reply, "没有需要保存的技能") {
			log.Printf("[SkillLearner] 审查完成：无需创建技能")
			return
		}

		// 检查是否真的执行了技能操作
		if containsSkillAction(reply) {
			log.Printf("[SkillLearner] ✨ 后台审查完成，已自动创建/更新技能")
		} else {
			log.Printf("[SkillLearner] 审查完成：%s", truncateStr(reply, 100))
		}
	}()
}

// containsSkillAction 检查回复中是否包含技能操作的迹象
func containsSkillAction(text string) bool {
	keywords := []string{
		"skill_install", "skill_update", "skill_delete",
		"技能已创建", "技能已更新", "技能已删除",
		"SKILL.md", "已安装技能",
	}
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// truncateStr 截断字符串
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// HistorySnapshot 获取对话历史的快照（用于传给后台审查）
func HistorySnapshot(history []*schema.Message) []*schema.Message {
	snapshot := make([]*schema.Message, len(history))
	copy(snapshot, history)
	return snapshot
}

// IntegrateSkillLearner 在 Agent 的 Run/AppendAssistantMessage 后调用
// 检查是否需要触发后台技能审查
func (a *Agent) CheckSkillLearning(ctx context.Context, reply string) {
	if a.skillLearner == nil {
		return
	}
	a.skillLearner.OnTurn(reply)
	if a.skillLearner.ShouldReview() {
		snapshot := HistorySnapshot(a.history)
		a.skillLearner.RunBackgroundReview(ctx, snapshot)
	}
}

// SetSkillLearner 设置技能自学习引擎
func (a *Agent) SetSkillLearner(l *SkillLearner) {
	a.skillLearner = l
}

// FormatSkillReviewSummary 格式化审查结果（用于通知用户）
func FormatSkillReviewSummary(reply string) string {
	if strings.Contains(reply, "没有需要保存的技能") {
		return ""
	}
	lines := strings.Split(reply, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len([]rune(line)) > 10 {
			return fmt.Sprintf("💡 技能学习: %s", truncateStr(line, 80))
		}
	}
	return ""
}
