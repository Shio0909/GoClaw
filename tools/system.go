package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// processTool 对应 OpenClaw 的 process - 查看和管理系统进程
func processTool() *ToolDef {
	return &ToolDef{
		Name:        "process_list",
		Description: "列出当前系统正在运行的进程（按 CPU/内存排序）",
		Parameters: []ParamDef{
			{Name: "filter", Type: "string", Description: "按进程名过滤（可选）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			filter, _ := args["filter"].(string)
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "tasklist")
			} else {
				cmd = exec.CommandContext(ctx, "ps", "aux", "--sort=-pcpu")
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("list processes: %w", err)
			}
			result := string(output)
			if filter != "" {
				var filtered []string
				for _, line := range strings.Split(result, "\n") {
					if strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
						filtered = append(filtered, line)
					}
				}
				result = strings.Join(filtered, "\n")
			}
			return result, nil
		},
	}
}

// cronTool 对应 OpenClaw 的 cron - 简易定时提醒
// 注意：这是一个简化版，只支持延时提醒，不支持 cron 表达式
func cronTool() *ToolDef {
	return &ToolDef{
		Name:        "reminder",
		Description: "设置一个延时提醒，到时间后会输出提醒消息",
		Parameters: []ParamDef{
			{Name: "message", Type: "string", Description: "提醒内容", Required: true},
			{Name: "delay_seconds", Type: "number", Description: "延迟秒数", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			message, _ := args["message"].(string)
			delay, _ := args["delay_seconds"].(float64)
			if delay <= 0 || delay > 3600 {
				return "", fmt.Errorf("delay must be between 1 and 3600 seconds")
			}

			go func() {
				time.Sleep(time.Duration(delay) * time.Second)
				fmt.Printf("\n⏰ 提醒: %s\n", message)
			}()

			return fmt.Sprintf("已设置提醒：%s（%d秒后）", message, int(delay)), nil
		},
	}
}

// envTool 查看/设置环境变量
func envTool() *ToolDef {
	return &ToolDef{
		Name:        "env",
		Description: "查看或搜索环境变量",
		Parameters: []ParamDef{
			{Name: "name", Type: "string", Description: "环境变量名（留空则列出所有）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name != "" {
				val := os.Getenv(name)
				if val == "" {
					return fmt.Sprintf("%s 未设置", name), nil
				}
				return fmt.Sprintf("%s=%s", name, val), nil
			}
			// 列出所有（过滤敏感信息）
			var result []string
			for _, env := range os.Environ() {
				lower := strings.ToLower(env)
				if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
					k, _, _ := strings.Cut(env, "=")
					result = append(result, k+"=***")
				} else {
					result = append(result, env)
				}
			}
			return strings.Join(result, "\n"), nil
		},
	}
}

// jsonParseTool 解析和查询 JSON
func jsonParseTool() *ToolDef {
	return &ToolDef{
		Name:        "json_parse",
		Description: "解析 JSON 字符串并格式化输出，或从 JSON 文件中提取数据",
		Parameters: []ParamDef{
			{Name: "input", Type: "string", Description: "JSON 字符串或文件路径", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]any) (string, error) {
			input, _ := args["input"].(string)

			// 尝试作为文件路径读取
			if data, err := os.ReadFile(input); err == nil {
				input = string(data)
			}

			var parsed any
			if err := json.Unmarshal([]byte(input), &parsed); err != nil {
				return "", fmt.Errorf("invalid JSON: %w", err)
			}

			pretty, err := json.MarshalIndent(parsed, "", "  ")
			if err != nil {
				return "", err
			}
			return string(pretty), nil
		},
	}
}
