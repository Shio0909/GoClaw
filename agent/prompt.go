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

// AgentName agent 名字（由 main 设置，对应 config.agent.name）
var AgentName string

// BuildSystemPrompt 构建完整的 system prompt
// 包含：身份 + 记忆上下文 + 技能 + 可用工具数量 + 行为规范 + 按需 MCP/技能工具说明
func BuildSystemPrompt(memMgr *memory.Manager, registry *tools.Registry) (string, error) {
	memCtx, err := memMgr.BuildContext()
	if err != nil {
		return "", fmt.Errorf("build memory context: %w", err)
	}

	var sb strings.Builder

	// 身份
	if AgentName != "" {
		sb.WriteString(fmt.Sprintf("你叫%s，是用户的私人 AI。", AgentName))
	} else {
		sb.WriteString("你是用户的私人 AI 助手。")
	}
	sb.WriteString("你有记忆，记得我们的对话历史和用户偏好。直接、不废话。\n\n")

	// 记忆上下文（动态注入）
	sb.WriteString(memCtx)

	sb.WriteString(fmt.Sprintf("你有 %d 个可用工具（详见 tool definitions），需要时直接调用。\n\n", len(registry.Names())))

	// 加载技能（动态注入）
	if SkillsDir != "" {
		if loaded, err := skills.LoadSkills(SkillsDir); err == nil && len(loaded) > 0 {
			sb.WriteString(skills.BuildSkillPrompt(loaded))
		}
	}

	sb.WriteString("=== 行为规范 ===\n")
	sb.WriteString("- 需要工具时直接调用，不要描述你打算做什么\n")
	sb.WriteString("- 先给结论，用户追问再展开；不重复用户的问题\n")
	sb.WriteString("- 危险操作（删文件、执行未知命令）先向用户确认\n")
	sb.WriteString("- 用用户的语言回答（中文回中文，英文回英文）\n")
	sb.WriteString("- 工具调用失败时简要说明并尝试替代方案\n")
	sb.WriteString(fmt.Sprintf("- 当前日期: %s\n", time.Now().Format("2006-01-02")))

	// MCP 工具安装说明 — 仅在相关工具已注册时出现
	_, hasMCPSearch := registry.Get("mcp_search")
	_, hasMCPInstall := registry.Get("mcp_install")
	if hasMCPSearch || hasMCPInstall {
		sb.WriteString("\n=== MCP 工具安装 ===\n")
		sb.WriteString("- 搜索：mcp_search（精选）→ mcp_marketplace_search（在线市场，如可用）→ web_search\n")
		sb.WriteString("- 非白名单包必须展示安全审查信息，等用户明确同意后再设置 confirmed=\"true\"\n")
		sb.WriteString("- 白名单包（@modelcontextprotocol/ 和精选列表）自动通过；安装后无需重启\n")
	}

	// 技能管理说明 — 仅在相关工具已注册时出现
	_, hasSkillInstall := registry.Get("skill_install")
	_, hasSkillUpdate := registry.Get("skill_update")
	if hasSkillInstall || hasSkillUpdate {
		sb.WriteString("\n=== 技能管理 ===\n")
		sb.WriteString("- 用户反复请求同类任务时，主动建议创建技能（skill_install），并预览内容等待确认\n")
		sb.WriteString("- 用户给出改进反馈时用 skill_update 更新技能；skill_list 查看已有技能\n")
	}

	// 沙箱限制
	if tools.Sandbox != "" {
		sb.WriteString("\n=== 安全限制 ===\n")
		sb.WriteString(fmt.Sprintf("- 文件写入/编辑/追加操作仅限于目录: %s\n", tools.Sandbox))
		sb.WriteString("- 读取文件（file_read）和列出目录（list_dir）不受此限制\n")
		sb.WriteString(fmt.Sprintf("- 写入操作请使用 %s 下的绝对路径\n", tools.Sandbox))
	}

	return sb.String(), nil
}
