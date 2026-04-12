package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewSkillInstallTool 创建技能安装工具
func NewSkillInstallTool() *ToolDef {
	return &ToolDef{
		Name:        "skill_install",
		Description: "创建一个新技能。会在 skills/<name>/SKILL.md 生成技能文件，下一轮对话自动加载",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "技能名称（英文，用作文件夹名，如 translate、summarize）", Required: true},
			{Name: "description", Type: "string", Description: "技能的简短描述", Required: true},
			{Name: "content", Type: "string", Description: "技能的完整 Markdown 内容（指令、流程、示例等）", Required: true},
			{Name: "confirmed", Type: "string", Description: "确认安装。首次调用会预览内容，用户确认后设为 \"true\" 才实际创建", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, _ := args["name"].(string)
			description, _ := args["description"].(string)
			content, _ := args["content"].(string)

			if name == "" || description == "" || content == "" {
				return "", fmt.Errorf("name, description, content 均为必填")
			}

			// 预览模式：首次调用展示内容，用户确认后才写入
			confirmed, _ := args["confirmed"].(string)
			if confirmed != "true" {
				var securityNote string
				if issues := scanSkillContent(content); len(issues) > 0 {
					securityNote = fmt.Sprintf("\n\n🔒 安全扫描发现问题：\n%s\n", strings.Join(issues, "\n"))
				}
				preview := fmt.Sprintf("📋 技能预览\n\n名称: %s\n描述: %s\n\n--- SKILL.md 内容 ---\n%s\n---%s\n\n⚠️ 技能会注入到系统提示词中，请确认内容安全。\n用户确认后，再次调用 skill_install 并设置 confirmed=\"true\"。", name, description, content, securityNote)
				return preview, nil
			}

			// 清理名称
			name = strings.ToLower(strings.TrimSpace(name))
			name = strings.ReplaceAll(name, " ", "_")

			skillDir := filepath.Join("skills", name)
			skillFile := filepath.Join(skillDir, "SKILL.md")

			// 检查是否已存在
			if _, err := os.Stat(skillFile); err == nil {
				return "", fmt.Errorf("技能 %s 已存在，请先删除 %s", name, skillDir)
			}

			// 创建目录
			if err := os.MkdirAll(skillDir, 0755); err != nil {
				return "", fmt.Errorf("创建目录失败: %w", err)
			}

			// 生成 SKILL.md
			md := fmt.Sprintf("---\nname: %s\ndescription: \"%s\"\nversion: \"1.0\"\n---\n\n%s\n", name, description, content)

			if err := os.WriteFile(skillFile, []byte(md), 0644); err != nil {
				return "", fmt.Errorf("写入文件失败: %w", err)
			}

			return fmt.Sprintf("已创建技能 %s → %s\n下一轮对话将自动加载此技能。", name, skillFile), nil
		},
	}
}
