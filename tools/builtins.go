package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

	// 代码搜索
	r.Register(grepSearchTool())
	r.Register(globSearchTool())

	// Git 操作
	r.Register(gitStatusTool())
	r.Register(gitLogTool())
	r.Register(gitDiffTool())

	// 技能管理
	r.Register(NewSkillInstallTool())
	RegisterSkillTools(r)
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
		Description: "列出指定目录下的文件和子目录。支持递归遍历和深度控制。",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "目录路径", Required: true},
			{Name: "recursive", Type: "boolean", Description: "是否递归列出子目录（默认 false）", Required: false},
			{Name: "max_depth", Type: "number", Description: "递归最大深度（默认 3，仅 recursive=true 时生效）", Required: false},
			{Name: "show_hidden", Type: "boolean", Description: "是否显示隐藏文件（默认 false）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				path = "."
			}
			recursive, _ := args["recursive"].(bool)
			showHidden, _ := args["show_hidden"].(bool)
			maxDepth := 3
			if md, ok := args["max_depth"].(float64); ok && md > 0 {
				maxDepth = int(md)
			}

			if !recursive {
				return listDirFlat(path, showHidden)
			}
			return listDirRecursive(path, showHidden, maxDepth)
		},
	}
}

// listDirFlat lists a single directory level
func listDirFlat(path string, showHidden bool) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if !showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
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
}

// listDirRecursive lists directory tree with depth control
func listDirRecursive(root string, showHidden bool, maxDepth int) (string, error) {
	var sb strings.Builder
	count := 0
	const maxEntries = 1000

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if count >= maxEntries {
			return filepath.SkipAll
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		// calculate depth
		depth := strings.Count(filepath.ToSlash(rel), "/")
		if depth >= maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// skip common noise dirs
		if d.IsDir() && (name == "node_modules" || name == "vendor" || name == "__pycache__") {
			return filepath.SkipDir
		}

		indent := strings.Repeat("  ", depth)
		if d.IsDir() {
			sb.WriteString(fmt.Sprintf("%s📁 %s/\n", indent, name))
		} else {
			info, _ := d.Info()
			if info != nil {
				sb.WriteString(fmt.Sprintf("%s📄 %s (%d bytes)\n", indent, name, info.Size()))
			} else {
				sb.WriteString(fmt.Sprintf("%s📄 %s\n", indent, name))
			}
		}
		count++
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return "", fmt.Errorf("walk dir: %w", err)
	}
	if count >= maxEntries {
		sb.WriteString(fmt.Sprintf("\n... 结果已截断（上限 %d 条）", maxEntries))
	}
	return sb.String(), nil
}

func shellTool() *ToolDef {
	return &ToolDef{
		Name:        "shell",
		Description: "执行 shell 命令并返回输出。支持超时控制和工作目录设置。Windows 用 cmd /C，Linux/Mac 用 sh -c",
		Parameters: []ParamDef{
			{Name: "command", Type: "string", Description: "要执行的命令", Required: true},
			{Name: "working_dir", Type: "string", Description: "工作目录（可选，默认当前目录）", Required: false},
			{Name: "timeout_seconds", Type: "number", Description: "超时秒数（可选，默认120秒，最大600秒）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			command, _ := args["command"].(string)
			if command == "" {
				return "", fmt.Errorf("command is required")
			}

			// 安全检查
			if err := ShellSecurity.CheckCommand(command); err != nil {
				return "", err
			}

			// 超时控制
			timeout := 120.0
			if t, ok := args["timeout_seconds"].(float64); ok && t > 0 {
				timeout = t
			}
			if timeout > 600 {
				timeout = 600
			}
			execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()

			var cmd *exec.Cmd
			if runtime.GOOS == "windows" {
				cmd = exec.CommandContext(execCtx, "cmd", "/C", command)
			} else {
				cmd = exec.CommandContext(execCtx, "sh", "-c", command)
			}

			// 工作目录
			if dir, ok := args["working_dir"].(string); ok && dir != "" {
				cmd.Dir = dir
			}

			output, err := cmd.CombinedOutput()
			result := string(output)

			// 输出截断（防止大输出撑爆上下文）
			const maxOutput = 50 * 1024 // 50KB
			if len(result) > maxOutput {
				truncated := len(result) - maxOutput
				result = result[:maxOutput/2] + fmt.Sprintf("\n\n... [截断 %d 字节] ...\n\n", truncated) + result[len(result)-maxOutput/2:]
			}

			result = strings.TrimSpace(result)

			if err != nil {
				if execCtx.Err() == context.DeadlineExceeded {
					return result, fmt.Errorf("命令超时（%d秒）: %s", int(timeout), command)
				}
				exitCode := -1
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
				return result, fmt.Errorf("exit code %d: %s", exitCode, result)
			}
			return result, nil
		},
	}
}
