package tools

import (
	"context"
	"fmt"
)

// ConfirmFunc 工具执行确认回调
// 返回 true 允许执行，false 拒绝执行
type ConfirmFunc func(toolName string, summary string) bool

type confirmKeyType struct{}

var confirmKey confirmKeyType

// WithConfirmFunc 将确认回调注入到 context 中
func WithConfirmFunc(ctx context.Context, fn ConfirmFunc) context.Context {
	return context.WithValue(ctx, confirmKey, fn)
}

// getConfirmFunc 从 context 中获取确认回调（如果有）
func getConfirmFunc(ctx context.Context) ConfirmFunc {
	if fn, ok := ctx.Value(confirmKey).(ConfirmFunc); ok {
		return fn
	}
	return nil
}

// dangerousTools 需要用户确认的工具集合
var dangerousTools = map[string]bool{
	"shell":          true,
	"process":        true,
	"file_write":     true,
	"file_edit":      true,
	"file_append":    true,
	"mcp_install":    true,
	"skill_install":  true,
}

// IsDangerousTool 判断工具是否需要确认
func IsDangerousTool(name string) bool {
	return dangerousTools[name]
}

// summarizeToolCall 生成工具调用的人类可读摘要
func summarizeToolCall(toolName string, args map[string]interface{}) string {
	switch toolName {
	case "shell":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			return fmt.Sprintf("执行命令: %s", cmd)
		}
	case "file_write":
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("写入文件: %s", path)
		}
	case "file_edit":
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("编辑文件: %s", path)
		}
	case "file_append":
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("追加文件: %s", path)
		}
	case "process":
		if cmd, ok := args["command"].(string); ok {
			return fmt.Sprintf("启动进程: %s", cmd)
		}
	case "mcp_install":
		if name, ok := args["name"].(string); ok {
			return fmt.Sprintf("安装 MCP 服务: %s", name)
		}
	case "skill_install":
		if name, ok := args["name"].(string); ok {
			return fmt.Sprintf("安装技能: %s", name)
		}
	}
	return fmt.Sprintf("调用工具: %s", toolName)
}

// requestConfirmation 请求用户确认工具调用
// 如果 context 中没有确认回调，默认允许执行
func requestConfirmation(ctx context.Context, toolName string, args map[string]interface{}) error {
	if !IsDangerousTool(toolName) {
		return nil
	}
	fn := getConfirmFunc(ctx)
	if fn == nil {
		return nil // no callback = auto-approve (e.g., HTTP mode)
	}
	summary := summarizeToolCall(toolName, args)
	if !fn(toolName, summary) {
		return fmt.Errorf("用户拒绝执行: %s", summary)
	}
	return nil
}
