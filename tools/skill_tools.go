package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goclaw/goclaw/skills"
)

// RegisterSkillTools 注册完整的技能自改进工具集
func RegisterSkillTools(r *Registry) {
	r.Register(skillListTool())
	r.Register(skillUpdateTool())
	r.Register(skillDeleteTool())
}

// skillListTool 列出所有已安装的技能
func skillListTool() *ToolDef {
	return &ToolDef{
		Name:        "skill_list",
		Description: "列出所有已安装的技能及其描述",
		Parameters:  []ParamDef{},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			loaded, err := skills.LoadSkills("skills")
			if err != nil {
				return "", fmt.Errorf("加载技能失败: %w", err)
			}
			if len(loaded) == 0 {
				return "当前没有安装任何技能。\n\n提示：使用 skill_install 创建新技能。", nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("已安装 %d 个技能：\n\n", len(loaded)))
			for _, s := range loaded {
				version := s.Version
				if version == "" {
					version = "-"
				}
				sb.WriteString(fmt.Sprintf("📦 %s (v%s)\n", s.Name, version))
				sb.WriteString(fmt.Sprintf("   %s\n", s.Description))
				if len(s.Requires.Tools) > 0 {
					sb.WriteString(fmt.Sprintf("   依赖工具: %s\n", strings.Join(s.Requires.Tools, ", ")))
				}
				if len(s.References) > 0 {
					refs := make([]string, 0, len(s.References))
					for name := range s.References {
						refs = append(refs, name)
					}
					sb.WriteString(fmt.Sprintf("   参考文件: %s\n", strings.Join(refs, ", ")))
				}
				sb.WriteString("\n")
			}
			return sb.String(), nil
		},
	}
}

// skillUpdateTool 更新已有技能
func skillUpdateTool() *ToolDef {
	return &ToolDef{
		Name:        "skill_update",
		Description: "更新已有技能的内容。可以修改描述、版本和正文内容",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "要更新的技能名称", Required: true},
			{Name: "description", Type: "string", Description: "新的技能描述（留空则保持不变）", Required: false},
			{Name: "content", Type: "string", Description: "新的 Markdown 正文内容", Required: true},
			{Name: "version", Type: "string", Description: "新版本号（如 1.1）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, _ := args["name"].(string)
			content, _ := args["content"].(string)
			description, _ := args["description"].(string)
			version, _ := args["version"].(string)

			if name == "" || content == "" {
				return "", fmt.Errorf("name 和 content 为必填")
			}

			name = strings.ToLower(strings.TrimSpace(name))
			skillFile := filepath.Join("skills", name, "SKILL.md")

			// 检查技能是否存在
			if _, err := os.Stat(skillFile); os.IsNotExist(err) {
				return "", fmt.Errorf("技能 %s 不存在，请使用 skill_install 创建", name)
			}

			// 读取现有技能获取默认值
			existing, err := skills.LoadSkills("skills")
			if err == nil {
				for _, s := range existing {
					if s.Name == name {
						if description == "" {
							description = s.Description
						}
						if version == "" {
							// 自动递增小版本号
							v := s.Version
							if v == "" {
								v = "1.0"
							}
							version = incrementVersion(v)
						}
						break
					}
				}
			}
			if description == "" {
				description = name
			}
			if version == "" {
				version = "1.1"
			}

			// 安全扫描
			if issues := scanSkillContent(content); len(issues) > 0 {
				return fmt.Sprintf("⚠️ 安全扫描发现问题：\n%s\n\n请修正后重试。", strings.Join(issues, "\n")), nil
			}

			// 写入更新后的 SKILL.md
			md := fmt.Sprintf("---\nname: %s\ndescription: \"%s\"\nversion: \"%s\"\n---\n\n%s\n", name, description, version, content)
			if err := os.WriteFile(skillFile, []byte(md), 0644); err != nil {
				return "", fmt.Errorf("写入文件失败: %w", err)
			}

			return fmt.Sprintf("✅ 技能 %s 已更新到 v%s\n下一轮对话将使用新版本。", name, version), nil
		},
	}
}

// skillDeleteTool 删除技能
func skillDeleteTool() *ToolDef {
	return &ToolDef{
		Name:        "skill_delete",
		Description: "删除一个已安装的技能",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "要删除的技能名称", Required: true},
			{Name: "confirmed", Type: "string", Description: "确认删除。首次调用会预览，设为 \"true\" 才实际删除", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", fmt.Errorf("name 为必填")
			}

			name = strings.ToLower(strings.TrimSpace(name))
			skillDir := filepath.Join("skills", name)

			if _, err := os.Stat(skillDir); os.IsNotExist(err) {
				return "", fmt.Errorf("技能 %s 不存在", name)
			}

			confirmed, _ := args["confirmed"].(string)
			if confirmed != "true" {
				return fmt.Sprintf("⚠️ 即将删除技能 %s（%s）\n\n确认删除请再次调用 skill_delete 并设置 confirmed=\"true\"", name, skillDir), nil
			}

			if err := os.RemoveAll(skillDir); err != nil {
				return "", fmt.Errorf("删除失败: %w", err)
			}
			return fmt.Sprintf("✅ 技能 %s 已删除", name), nil
		},
	}
}

// scanSkillContent 基本安全扫描：检查技能内容是否包含可疑指令
func scanSkillContent(content string) []string {
	var issues []string
	lower := strings.ToLower(content)

	// 检查危险指令
	dangerousPatterns := []struct {
		pattern string
		reason  string
	}{
		{"rm -rf /", "尝试删除根目录"},
		{"format c:", "尝试格式化磁盘"},
		{":(){:|:&};:", "Fork 炸弹"},
		{"chmod 777", "过度开放文件权限"},
		{"curl | sh", "从网络下载并执行脚本"},
		{"curl|sh", "从网络下载并执行脚本"},
		{"wget | sh", "从网络下载并执行脚本"},
		{"wget|sh", "从网络下载并执行脚本"},
		{"| sh", "管道执行 shell"},
		{"|sh", "管道执行 shell"},
		{"eval(", "动态代码执行"},
		{"__import__('os')", "Python 系统调用注入"},
		{"忽略上述", "提示词注入（中文）"},
		{"ignore previous", "提示词注入（英文）"},
		{"ignore above", "提示词注入（英文）"},
		{"disregard", "提示词注入（英文）"},
		{"你是一个新的ai", "角色劫持（中文）"},
		{"you are now", "角色劫持（英文）"},
	}

	for _, dp := range dangerousPatterns {
		if strings.Contains(lower, dp.pattern) {
			issues = append(issues, fmt.Sprintf("  - ⛔ %s: 检测到「%s」", dp.reason, dp.pattern))
		}
	}

	return issues
}

// incrementVersion 简单版本号递增（1.0 → 1.1，2.3 → 2.4）
func incrementVersion(v string) string {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return v + ".1"
	}
	minor := 0
	for _, ch := range parts[1] {
		if ch >= '0' && ch <= '9' {
			minor = minor*10 + int(ch-'0')
		} else {
			break
		}
	}
	return fmt.Sprintf("%s.%d", parts[0], minor+1)
}
