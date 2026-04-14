package agent

import (
	"errors"
	"testing"

	"github.com/cloudwego/eino/components/model"
)

func TestShouldFallback(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		expect bool
	}{
		{"server error 503", errors.New("HTTP 503 Service Unavailable"), true},
		{"server error 500", errors.New("HTTP 500 Internal Server Error"), true},
		{"timeout", errors.New("context deadline exceeded"), true},
		{"rate limit 429", errors.New("HTTP 429 Too Many Requests"), true},
		{"billing 402", errors.New("HTTP 402 Payment Required"), true},
		{"auth error 401", errors.New("HTTP 401 Unauthorized"), false},
		{"unknown error", errors.New("some random error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFallback(tt.err)
			if got != tt.expect {
				t.Errorf("shouldFallback(%q) = %v, want %v", tt.err, got, tt.expect)
			}
		})
	}
}

func TestFallbackConfig(t *testing.T) {
	a := &Agent{}

	// No fallback config → nil
	if a.fallbackCfg != nil {
		t.Error("expected nil fallback config")
	}

	// Set fallback config
	fb := &FallbackConfig{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "test-key",
	}
	a.SetFallbackConfig(fb)

	if a.fallbackCfg == nil {
		t.Fatal("expected non-nil fallback config")
	}
	if a.fallbackCfg.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", a.fallbackCfg.Model)
	}
}

func TestFallbackAPIKeyInheritance(t *testing.T) {
	a := &Agent{
		cfg: Config{APIKey: "primary-key"},
	}
	fb := &FallbackConfig{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		// APIKey is empty — should inherit primary
	}
	a.SetFallbackConfig(fb)

	if a.fallbackCfg.APIKey != "" {
		t.Errorf("fallback key should be empty (inherits at runtime)")
	}
}

func TestRunWithFallbackNoConfig(t *testing.T) {
	a := &Agent{}
	primaryErr := errors.New("HTTP 503 Service Unavailable")

	// No fallback config → should return primary error
	err := a.runWithFallback(nil, primaryErr, func(m model.ToolCallingChatModel) error {
		t.Fatal("should not be called")
		return nil
	})

	if err != primaryErr {
		t.Errorf("expected primary error, got: %v", err)
	}
}

func TestRunWithFallbackNonRetryableError(t *testing.T) {
	a := &Agent{
		fallbackCfg: &FallbackConfig{
			Provider: "openai",
			Model:    "test",
		},
	}
	// Auth error → should NOT trigger fallback
	primaryErr := errors.New("HTTP 401 Unauthorized")

	called := false
	err := a.runWithFallback(nil, primaryErr, func(m model.ToolCallingChatModel) error {
		called = true
		return nil
	})

	if called {
		t.Error("fallback should not be called for auth errors")
	}
	if err != primaryErr {
		t.Errorf("expected primary error, got: %v", err)
	}
}
