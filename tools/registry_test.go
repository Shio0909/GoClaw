package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRegistryBasic(t *testing.T) {
	r := NewRegistry()

	// 注册一个测试工具
	r.Register(&ToolDef{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters: []ParamDef{
			{Name: "input", Type: "string", Description: "test input", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "result:" + args["input"].(string), nil
		},
	})

	// Get
	tool, ok := r.Get("test_tool")
	if !ok {
		t.Fatal("expected to find test_tool")
	}
	if tool.Name != "test_tool" {
		t.Fatalf("expected name test_tool, got %s", tool.Name)
	}

	// Get non-existent
	_, ok = r.Get("nonexistent")
	if ok {
		t.Fatal("expected not to find nonexistent tool")
	}

	// Execute
	result, err := r.Execute(context.Background(), "test_tool", map[string]interface{}{"input": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "result:hello" {
		t.Fatalf("expected result:hello, got %s", result)
	}

	// Execute non-existent
	_, err = r.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent tool")
	}

	// Names
	names := r.Names()
	if len(names) != 1 || names[0] != "test_tool" {
		t.Fatalf("expected [test_tool], got %v", names)
	}
}

func TestToolTimeout(t *testing.T) {
	r := NewRegistry()
	r.Register(&ToolDef{
		Name:        "slow_tool",
		Description: "Takes too long",
		Timeout:     50 * time.Millisecond,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			select {
			case <-time.After(5 * time.Second):
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})

	_, err := r.Execute(context.Background(), "slow_tool", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Errorf("expected deadline exceeded, got: %v", err)
	}
}

func TestDefaultTimeout(t *testing.T) {
	r := NewRegistry()
	r.SetDefaultTimeout(50 * time.Millisecond)
	r.Register(&ToolDef{
		Name: "no_timeout_tool",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			select {
			case <-time.After(5 * time.Second):
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})

	_, err := r.Execute(context.Background(), "no_timeout_tool", nil)
	if err == nil {
		t.Fatal("expected timeout error from default timeout")
	}
}

func TestToolTimeoutOverridesDefault(t *testing.T) {
	r := NewRegistry()
	r.SetDefaultTimeout(1 * time.Millisecond)
	r.Register(&ToolDef{
		Name:    "fast_tool",
		Timeout: 2 * time.Second,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "ok", nil
		},
	})

	result, err := r.Execute(context.Background(), "fast_tool", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected ok, got %s", result)
	}
}
