package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/skills"
	"github.com/goclaw/goclaw/tools"
)

// SkillsDir 技能文件目录（由 main 设置）
var SkillsDir string

// BuildSystemPrompt 构建完整的 system prompt
// 包含：记忆上下文 + 技能 + 可用工具描述 + 行为指令
func BuildSystemPrompt(memMgr *memory.Manager, registry *tools.Registry) (string, error) {
	memCtx, err := memMgr.BuildContext()
	if err != nil {
		return "", fmt.Errorf("build memory context: %w", err)
	}

	toolDescs := buildToolDescriptions(registry)

	var sb strings.Builder
	sb.WriteString(memCtx)
	sb.WriteString("=== 可用工具 ===\n")
	sb.WriteString(toolDescs)
	sb.WriteString("\n\n")

	// 加载技能
	if SkillsDir != "" {
		if loaded, err := skills.LoadSkills(SkillsDir); err == nil && len(loaded) > 0 {
			sb.WriteString(skills.BuildSkillPrompt(loaded))
		}
	}

	sb.WriteString("=== 行为指令 ===\n")
	sb.WriteString("- 需要使用工具时直接调用，不要先描述你打算做什么\n")
	sb.WriteString("- 可以连续调用多个工具来完成复杂任务\n")
	sb.WriteString("- 对危险操作（删除文件、执行未知命令）先向用户确认\n")
	sb.WriteString("- 回答简洁直接，先给结论，用户追问再展开\n")
	sb.WriteString("- 不要重复用户的问题，不要过度解释工具调用过程\n")
	sb.WriteString("- 如果工具调用失败，简要说明原因并尝试替代方案\n")
	sb.WriteString(fmt.Sprintf("- 当前日期: %s\n", time.Now().Format("2006-01-02")))

	// 沙箱限制
	if tools.Sandbox != "" {
		sb.WriteString(fmt.Sprintf("\n=== 安全限制 ===\n"))
		sb.WriteString(fmt.Sprintf("- 文件写入/编辑/追加操作仅限于目录: %s\n", tools.Sandbox))
		sb.WriteString("- 读取文件不受此限制\n")
		sb.WriteString("- 所有文件操作路径请使用该目录下的绝对路径\n")
	}

	return sb.String(), nil
}

func buildToolDescriptions(registry *tools.Registry) string {
	var sb strings.Builder
	for _, name := range registry.Names() {
		t, _ := registry.Get(name)
		if t != nil {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
		}
	}
	return sb.String()
}
