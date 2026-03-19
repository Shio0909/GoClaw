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

	var sb strings.Builder
	sb.WriteString(memCtx)
	sb.WriteString(fmt.Sprintf("你有 %d 个可用工具（详见 tool definitions），需要时直接调用。\n\n", len(registry.Names())))

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
	sb.WriteString("\n=== MCP Server 与技能安装 ===\n")
	sb.WriteString("- 当你发现自己缺少某种能力（如数据库操作、特定 API 访问），主动建议搜索并安装相关 MCP Server\n")
	sb.WriteString("- 搜索链路：先用 mcp_search 搜索精选列表 → 没有结果时用 mcp_marketplace_search 搜索在线市场（如果可用）→ 都没有则用 web_search 搜索\n")
	sb.WriteString("- 如果需要 API Key 或环境变量，主动询问用户\n")
	sb.WriteString("- 安装非白名单包时，mcp_install 会返回安全审查信息，你必须将审查信息完整展示给用户并等待确认\n")
	sb.WriteString("- 用户明确同意后（如「装吧」「yes」「确认」「安装」），再次调用 mcp_install 并设置 confirmed=\"true\"\n")
	sb.WriteString("- 白名单包（@modelcontextprotocol/ 官方包和精选列表）会自动通过审查\n")
	sb.WriteString("- 安装后当前对话立即可用新工具，无需重启\n")
	sb.WriteString("- 技能安装同理：skill_install 会先预览完整内容，用户确认后才写入文件\n")

	// 沙箱限制
	if tools.Sandbox != "" {
		sb.WriteString("\n=== 安全限制 ===\n")
		sb.WriteString(fmt.Sprintf("- 文件写入/编辑/追加操作仅限于目录: %s\n", tools.Sandbox))
		sb.WriteString("- 读取文件（file_read）和列出目录（list_dir）不受此限制，可以读取任意路径\n")
		sb.WriteString(fmt.Sprintf("- 写入操作的路径请使用 %s 下的绝对路径\n", tools.Sandbox))
	}

	return sb.String(), nil
}
