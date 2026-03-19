package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// fileEditTool 对应 OpenClaw 的 edit - 精确替换文件内容
func fileEditTool() *ToolDef {
	return &ToolDef{
		Name:        "file_edit",
		Description: "编辑文件：将文件中匹配的旧内容替换为新内容（精确字符串匹配）",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "文件路径", Required: true},
			{Name: "old_text", Type: "string", Description: "要被替换的原始文本", Required: true},
			{Name: "new_text", Type: "string", Description: "替换后的新文本", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			oldText, _ := args["old_text"].(string)
			newText, _ := args["new_text"].(string)

			if err := checkSandbox(path, false); err != nil {
				return "", err
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read file: %w", err)
			}

			content := string(data)
			if !strings.Contains(content, oldText) {
				return "", fmt.Errorf("old_text not found in file")
			}

			newContent := strings.Replace(content, oldText, newText, 1)
			if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
				return "", fmt.Errorf("write file: %w", err)
			}
			return fmt.Sprintf("已替换 %s 中的内容", path), nil
		},
	}
}

// fileAppendTool 追加内容到文件
func fileAppendTool() *ToolDef {
	return &ToolDef{
		Name:        "file_append",
		Description: "在文件末尾追加内容",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "文件路径", Required: true},
			{Name: "content", Type: "string", Description: "要追加的内容", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)

			if err := checkSandbox(path, false); err != nil {
				return "", err
			}

			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return "", fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

			if _, err := f.WriteString(content); err != nil {
				return "", fmt.Errorf("append: %w", err)
			}
			return fmt.Sprintf("已追加 %d 字节到 %s", len(content), path), nil
		},
	}
}
