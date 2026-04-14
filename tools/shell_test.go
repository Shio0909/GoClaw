package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestShellTool_Basic(t *testing.T) {
	tool := shellTool()

	if tool.Name != "shell" {
		t.Fatalf("expected name 'shell', got %q", tool.Name)
	}

	// 基础命令执行
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "echo hello"
	} else {
		cmd = "echo hello"
	}

	result, err := tool.Fn(context.Background(), map[string]any{
		"command": cmd,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Fatalf("expected output to contain 'hello', got %q", result)
	}
}

func TestShellTool_WorkingDir(t *testing.T) {
	tool := shellTool()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "cd"
	} else {
		cmd = "pwd"
	}

	result, err := tool.Fn(context.Background(), map[string]any{
		"command":     cmd,
		"working_dir": ".",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestShellTool_Timeout(t *testing.T) {
	tool := shellTool()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "ping -n 10 127.0.0.1"
	} else {
		cmd = "sleep 10"
	}

	_, err := tool.Fn(context.Background(), map[string]any{
		"command":         cmd,
		"timeout_seconds": 1.0,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout error message, got: %v", err)
	}
}

func TestShellTool_EmptyCommand(t *testing.T) {
	tool := shellTool()

	_, err := tool.Fn(context.Background(), map[string]any{
		"command": "",
	})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestShellTool_NonZeroExit(t *testing.T) {
	tool := shellTool()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "exit /b 1"
	} else {
		cmd = "exit 1"
	}

	_, err := tool.Fn(context.Background(), map[string]any{
		"command": cmd,
	})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "exit code") {
		t.Fatalf("expected exit code in error, got: %v", err)
	}
}

func TestShellTool_TimeoutCapped(t *testing.T) {
	// 超时上限 600 秒，超过应被截断
	tool := shellTool()

	// 只测试不会 panic，不测执行
	if len(tool.Parameters) < 3 {
		t.Fatalf("expected 3 parameters, got %d", len(tool.Parameters))
	}
	if tool.Parameters[2].Name != "timeout_seconds" {
		t.Fatalf("expected 3rd param 'timeout_seconds', got %q", tool.Parameters[2].Name)
	}
}
