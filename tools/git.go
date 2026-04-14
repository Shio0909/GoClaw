package tools

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// gitStatusTool 查看 Git 仓库状态
func gitStatusTool() *ToolDef {
	return &ToolDef{
		Name:        "git_status",
		Description: "查看 Git 仓库当前状态（修改文件、暂存区、分支信息）",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "仓库路径（默认当前目录）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			dir, _ := args["path"].(string)
			if dir == "" {
				dir = "."
			}
			return runGit(ctx, dir, "status", "--short", "--branch")
		},
	}
}

// gitLogTool 查看 Git 提交历史
func gitLogTool() *ToolDef {
	return &ToolDef{
		Name:        "git_log",
		Description: "查看 Git 提交历史。支持限制条数、指定文件、格式化输出。",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "仓库路径（默认当前目录）", Required: false},
			{Name: "count", Type: "number", Description: "显示的提交数（默认 20）", Required: false},
			{Name: "file", Type: "string", Description: "只显示指定文件的历史", Required: false},
			{Name: "oneline", Type: "boolean", Description: "单行格式（默认 true）", Required: false},
			{Name: "author", Type: "string", Description: "按作者过滤", Required: false},
			{Name: "since", Type: "string", Description: "起始日期（如 2024-01-01 或 1.week.ago）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			dir, _ := args["path"].(string)
			if dir == "" {
				dir = "."
			}

			count := 20
			if n, ok := args["count"].(float64); ok && n > 0 {
				count = int(n)
			}

			oneline := true
			if ol, ok := args["oneline"].(bool); ok {
				oneline = ol
			}

			gitArgs := []string{"log", fmt.Sprintf("-n%d", count)}

			if oneline {
				gitArgs = append(gitArgs, "--oneline", "--decorate")
			} else {
				gitArgs = append(gitArgs, "--format=commit %H%nAuthor: %an <%ae>%nDate:   %ad%n%n    %s%n", "--date=short")
			}

			if author, ok := args["author"].(string); ok && author != "" {
				gitArgs = append(gitArgs, fmt.Sprintf("--author=%s", author))
			}
			if since, ok := args["since"].(string); ok && since != "" {
				gitArgs = append(gitArgs, fmt.Sprintf("--since=%s", since))
			}

			// file filter must come after --
			if file, ok := args["file"].(string); ok && file != "" {
				gitArgs = append(gitArgs, "--", file)
			}

			return runGit(ctx, dir, gitArgs...)
		},
	}
}

// gitDiffTool 查看 Git 变更内容
func gitDiffTool() *ToolDef {
	return &ToolDef{
		Name:        "git_diff",
		Description: "查看 Git 变更内容。支持工作区差异、暂存区差异、指定 commit 差异。",
		Parameters: []ParamDef{
			{Name: "path", Type: "string", Description: "仓库路径（默认当前目录）", Required: false},
			{Name: "staged", Type: "boolean", Description: "查看暂存区差异（git diff --staged）", Required: false},
			{Name: "commit", Type: "string", Description: "与指定 commit 比较（如 HEAD~3, abc123）", Required: false},
			{Name: "file", Type: "string", Description: "只查看指定文件的差异", Required: false},
			{Name: "stat_only", Type: "boolean", Description: "只显示统计信息（--stat）", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			dir, _ := args["path"].(string)
			if dir == "" {
				dir = "."
			}

			gitArgs := []string{"diff"}

			if staged, ok := args["staged"].(bool); ok && staged {
				gitArgs = append(gitArgs, "--staged")
			}

			if commit, ok := args["commit"].(string); ok && commit != "" {
				gitArgs = append(gitArgs, commit)
			}

			if statOnly, ok := args["stat_only"].(bool); ok && statOnly {
				gitArgs = append(gitArgs, "--stat")
			}

			if file, ok := args["file"].(string); ok && file != "" {
				gitArgs = append(gitArgs, "--", file)
			}

			result, err := runGit(ctx, dir, gitArgs...)
			if err != nil {
				return result, err
			}
			if strings.TrimSpace(result) == "" {
				return "没有变更内容", nil
			}
			return result, nil
		},
	}
}

// runGit executes a git command and returns its output
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// prepend --no-pager as a top-level git flag (before subcommand)
	fullArgs := append([]string{"--no-pager"}, args...)

	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = dir

	cmd.Env = append(cmd.Environ(), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0")
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}

	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		if result != "" {
			return "", fmt.Errorf("git error: %s", result)
		}
		return "", fmt.Errorf("git error: %w", err)
	}
	return result, nil
}
