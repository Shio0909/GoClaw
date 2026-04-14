package tools

import (
	"context"
	"testing"
)

func TestIsDangerousTool(t *testing.T) {
	dangerous := []string{"shell", "process", "file_write", "file_edit", "file_append", "mcp_install"}
	for _, name := range dangerous {
		if !IsDangerousTool(name) {
			t.Errorf("%s should be dangerous", name)
		}
	}

	safe := []string{"file_read", "list_dir", "grep_search", "git_status", "json_parse"}
	for _, name := range safe {
		if IsDangerousTool(name) {
			t.Errorf("%s should NOT be dangerous", name)
		}
	}
}

func TestSummarizeToolCall(t *testing.T) {
	tests := []struct {
		tool    string
		args    map[string]interface{}
		want    string
	}{
		{"shell", map[string]interface{}{"command": "ls -la"}, "执行命令: ls -la"},
		{"file_write", map[string]interface{}{"path": "/tmp/test.txt"}, "写入文件: /tmp/test.txt"},
		{"file_edit", map[string]interface{}{"path": "main.go"}, "编辑文件: main.go"},
		{"unknown_tool", map[string]interface{}{}, "调用工具: unknown_tool"},
	}
	for _, tt := range tests {
		got := summarizeToolCall(tt.tool, tt.args)
		if got != tt.want {
			t.Errorf("summarizeToolCall(%s) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestRequestConfirmation_SafeTool(t *testing.T) {
	ctx := context.Background()
	err := requestConfirmation(ctx, "file_read", nil)
	if err != nil {
		t.Errorf("safe tool should not need confirmation, got: %v", err)
	}
}

func TestRequestConfirmation_NoCallback(t *testing.T) {
	ctx := context.Background()
	err := requestConfirmation(ctx, "shell", map[string]interface{}{"command": "rm -rf /"})
	if err != nil {
		t.Errorf("no callback = auto-approve, got: %v", err)
	}
}

func TestRequestConfirmation_Approved(t *testing.T) {
	ctx := WithConfirmFunc(context.Background(), func(name, summary string) bool {
		return true
	})
	err := requestConfirmation(ctx, "shell", map[string]interface{}{"command": "echo hi"})
	if err != nil {
		t.Errorf("approved should not error, got: %v", err)
	}
}

func TestRequestConfirmation_Denied(t *testing.T) {
	ctx := WithConfirmFunc(context.Background(), func(name, summary string) bool {
		return false
	})
	err := requestConfirmation(ctx, "shell", map[string]interface{}{"command": "rm -rf /"})
	if err == nil {
		t.Error("denied should return error")
	}
}

func TestConfirmFuncContext(t *testing.T) {
	called := false
	fn := func(name, summary string) bool {
		called = true
		return true
	}
	ctx := WithConfirmFunc(context.Background(), fn)
	got := getConfirmFunc(ctx)
	if got == nil {
		t.Fatal("expected ConfirmFunc in context")
	}
	got("test", "test")
	if !called {
		t.Error("ConfirmFunc was not called")
	}

	// No func in plain context
	if getConfirmFunc(context.Background()) != nil {
		t.Error("expected nil for plain context")
	}
}
