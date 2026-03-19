package tools

import (
	"context"
	"testing"
)

func TestEinoToolAdapter(t *testing.T) {
	def := &ToolDef{
		Name:        "echo",
		Description: "Echo back the input",
		Parameters: []ParamDef{
			{Name: "text", Type: "string", Description: "text to echo", Required: true},
			{Name: "count", Type: "integer", Description: "repeat count", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return args["text"].(string), nil
		},
	}

	einoTool := NewEinoTool(def)

	// Test Info
	ctx := context.Background()
	info, err := einoTool.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Name != "echo" {
		t.Fatalf("expected name echo, got %s", info.Name)
	}
	if info.Desc != "Echo back the input" {
		t.Fatalf("expected desc 'Echo back the input', got %s", info.Desc)
	}

	// Test InvokableRun
	result, err := einoTool.InvokableRun(ctx, `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error: %v", err)
	}
	if result != "hello" {
		t.Fatalf("expected hello, got %s", result)
	}

	// Test InvokableRun with empty args
	defNoArgs := &ToolDef{
		Name:        "ping",
		Description: "Ping",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "pong", nil
		},
	}
	pingTool := NewEinoTool(defNoArgs)
	result, err = pingTool.InvokableRun(ctx, "{}")
	if err != nil {
		t.Fatalf("InvokableRun({}) error: %v", err)
	}
	if result != "pong" {
		t.Fatalf("expected pong, got %s", result)
	}
}

func TestToEinoTools(t *testing.T) {
	r := NewRegistry()
	r.Register(&ToolDef{
		Name: "a", Description: "tool a",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) { return "a", nil },
	})
	r.Register(&ToolDef{
		Name: "b", Description: "tool b",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) { return "b", nil },
	})

	einoTools := r.ToEinoTools()
	if len(einoTools) != 2 {
		t.Fatalf("expected 2 eino tools, got %d", len(einoTools))
	}
}
