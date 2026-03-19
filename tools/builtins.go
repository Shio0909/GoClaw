package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RegisterBuiltins 注册所有内置工具
func RegisterBuiltins(r *Registry) {
	// 文件操作 (对应 OpenClaw: read, write, edit)
	r.Register(fileReadTool())
	r.Register(fileWriteTool())
	r.Register(fileEditTool())
	r.Register(fileAppendTool())
	r.Register(listDirTool())

	// 系统操作 (对应 OpenClaw: exec, process)
	r.Register(shellTool())
	r.Register(processTool())
	r.Register(envTool())

	// 实用工具
	r.Register(cronTool())
	r.Register(jsonParseTool())

	// 网络工具 (对应 OpenClaw: web_fetch, http)
	r.Register(NewWebFetchTool())
	r.Register(NewHTTPRequestTool())

	// MCP Server 管理
	r.Register(NewMCPSearchTool())
	r.Register(NewMCPInstallTool(r))
	r.Register(NewMCPUninstallTool(r))

	// 技能管理
	r.Register(NewSkillInstallTool())
}

func fileReadTool() *ToolDef {
	return &ToolDef{
		Name:        "file_read",
		Description: "读取指定路径的文件内容",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "文件路径", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			// 读操作不受沙箱限制
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read file: %w", err)
			}
			return string(data), nil
		},
	}
}

func fileWriteTool() *ToolDef {
	return &ToolDef{
		Name:        "file_write",
		Description: "将内容写入指定路径的文件",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "文件路径", Required: true},
			{Name: "content", Type: "string", Description: "要写入的内容", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			if err := checkSandbox(path, false); err != nil {
				return "", err
			}
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", fmt.Errorf("create dir: %w", err)
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write file: %w", err)
			}
			return fmt.Sprintf("已写入 %d 字节到 %s", len(content), path), nil
		},
	}
}

func listDirTool() *ToolDef {
	return &ToolDef{
		Name:        "list_dir",
		Description: "列出指定目录下的文件和子目录",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "目录路径", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				path = "."
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return "", fmt.Errorf("read dir: %w", err)
			}
			var sb strings.Builder
			for _, e := range entries {
				info, _ := e.Info()
				if e.IsDir() {
					sb.WriteString(fmt.Sprintf("[DIR]  %s/\n", e.Name()))
				} else if info != nil {
					sb.WriteString(fmt.Sprintf("[FILE] %s (%d bytes)\n", e.Name(), info.Size()))
				} else {
					sb.WriteString(fmt.Sprintf("[FILE] %s\n", e.Name()))
				}
			}
			return sb.String(), nil
		},
	}
}

func shellTool() *ToolDef {
	return &ToolDef{
		Name:        "shell",
		Description: "执行 shell 命令并返回输出",
		Parameters: []ParamDef{
			{Name: "command", Type: "string", Description: "要执行的命令", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			command, _ := args["command"].(string)
			if command == "" {
				return "", fmt.Errorf("command is required")
			}
			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(ctx, "cmd", "/C", command)
			} else {
				cmd = exec.CommandContext(ctx, "sh", "-c", command)
			}
			output, err := cmd.CombinedOutput()
			result := strings.TrimSpace(string(output))
			if err != nil {
				return result, fmt.Errorf("command failed: %w\noutput: %s", err, result)
			}
			return result, nil
		},
	}
}
