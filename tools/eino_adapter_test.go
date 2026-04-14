package tools

import (
	"context"
	"fmt"
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

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"permanent", fmt.Errorf("file not found"), false},
		{"timeout", fmt.Errorf("i/o timeout"), true},
		{"connection reset", fmt.Errorf("read tcp: connection reset by peer"), true},
		{"503", fmt.Errorf("HTTP 503 Service Unavailable"), true},
		{"502", fmt.Errorf("502 Bad Gateway"), true},
		{"429", fmt.Errorf("429 Too Many Requests"), true},
		{"EOF", fmt.Errorf("unexpected EOF"), true},
		{"broken pipe", fmt.Errorf("write: broken pipe"), true},
		{"tls", fmt.Errorf("TLS handshake timeout"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientError(tt.err)
			if got != tt.want {
				t.Errorf("isTransientError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryableToolRetries(t *testing.T) {
	calls := 0
	def := &ToolDef{
		Name:        "flaky",
		Description: "Fails twice then succeeds",
		Retryable:   true,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			calls++
			if calls < 3 {
				return "", fmt.Errorf("connection reset by peer")
			}
			return "ok", nil
		},
	}
	et := NewEinoTool(def)
	result, err := et.InvokableRun(context.Background(), "{}")
	if err != nil {
		t.Fatalf("expected no error after retries, got: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected 'ok', got %q", result)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestNonRetryableToolNoRetry(t *testing.T) {
	calls := 0
	def := &ToolDef{
		Name:        "strict",
		Description: "Not retryable",
		Retryable:   false,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			calls++
			return "", fmt.Errorf("connection reset by peer")
		},
	}
	et := NewEinoTool(def)
	_, err := et.InvokableRun(context.Background(), "{}")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call for non-retryable, got %d", calls)
	}
}

func TestRetryableExhaustsAttempts(t *testing.T) {
	calls := 0
	def := &ToolDef{
		Name:      "always_fails",
		Retryable: true,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			calls++
			return "", fmt.Errorf("503 service unavailable")
		},
	}
	et := NewEinoTool(def)
	_, err := et.InvokableRun(context.Background(), "{}")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != toolRetryMax {
		t.Fatalf("expected %d calls, got %d", toolRetryMax, calls)
	}
}

func TestRetryablePermanentErrorNoRetry(t *testing.T) {
	calls := 0
	def := &ToolDef{
		Name:      "permanent_fail",
		Retryable: true,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			calls++
			return "", fmt.Errorf("invalid argument: path is required")
		},
	}
	et := NewEinoTool(def)
	_, err := et.InvokableRun(context.Background(), "{}")
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("permanent error should not retry, expected 1 call, got %d", calls)
	}
}
