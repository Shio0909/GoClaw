package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sk-abc123456789", "6789"},
		{"abcd", "****"},
		{"ab", "****"},
		{"", "****"},
		{"12345", "2345"},
	}
	for _, tt := range tests {
		got := maskKey(tt.input)
		if got != tt.expected {
			t.Errorf("maskKey(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRunWithRetry_Success(t *testing.T) {
	a := &Agent{cfg: Config{APIKey: "test-key"}}
	calls := 0
	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRunWithRetry_SuccessRecordsPool(t *testing.T) {
	pool := NewCredentialPool("round_robin")
	pool.AddKey("key-1", "openai", "https://api.example.com")

	a := &Agent{
		cfg:         Config{APIKey: "key-1"},
		retryConfig: &RetryConfig{MaxAttempts: 2, Pool: pool},
	}
	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithRetry_NonRetriable400(t *testing.T) {
	a := &Agent{cfg: Config{APIKey: "test-key"}}
	calls := 0
	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		calls++
		return errors.New("HTTP 400: bad request - invalid json")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (400 is non-retriable), got %d", calls)
	}
}

func TestRunWithRetry_NonRetriableAuth(t *testing.T) {
	a := &Agent{cfg: Config{APIKey: "bad-key"}}
	calls := 0
	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		calls++
		return errors.New("HTTP 401: invalid api key")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// Auth errors without pool should not retry
	if calls != 1 {
		t.Errorf("expected 1 call (auth without pool), got %d", calls)
	}
}

func TestRunWithRetry_KeyRotation(t *testing.T) {
	pool := NewCredentialPool("round_robin")
	pool.AddKey("key-1", "openai", "https://api.example.com")
	pool.AddKey("key-2", "openai", "https://api.example.com")

	a := &Agent{
		cfg:         Config{APIKey: "key-1"},
		retryConfig: &RetryConfig{MaxAttempts: 3, Pool: pool},
	}

	usedKeys := []string{}
	calls := 0
	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		calls++
		usedKeys = append(usedKeys, cfg.APIKey)
		if calls == 1 {
			return errors.New("HTTP 429: rate limit exceeded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after key rotation, got: %v", err)
	}
	if len(usedKeys) != 2 {
		t.Fatalf("expected 2 key uses, got %d: %v", len(usedKeys), usedKeys)
	}
	if usedKeys[0] == usedKeys[1] {
		t.Error("expected key rotation, but same key was used twice")
	}
}

func TestRunWithRetry_PoolAllKeysExhausted(t *testing.T) {
	pool := NewCredentialPool("round_robin")
	pool.AddKey("only-key", "openai", "https://api.example.com")

	a := &Agent{
		cfg:         Config{APIKey: "only-key"},
		retryConfig: &RetryConfig{MaxAttempts: 3, Pool: pool},
	}

	err := a.runWithRetry(context.Background(), func(cfg Config) error {
		return errors.New("HTTP 401: unauthorized - invalid api key")
	})
	if err == nil {
		t.Fatal("expected error when all keys exhausted")
	}
	if !strings.Contains(err.Error(), "均不可用") {
		t.Errorf("expected '均不可用' in error, got: %v", err)
	}
}

func TestRunWithRetry_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{
		cfg:         Config{APIKey: "test-key"},
		retryConfig: &RetryConfig{MaxAttempts: 5},
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := a.runWithRetry(ctx, func(cfg Config) error {
		return errors.New("HTTP 503: service unavailable")
	})
	if err == nil {
		t.Fatal("expected error on context cancel")
	}
}

func TestRunWithRetry_DefaultMaxAttempts(t *testing.T) {
	a := &Agent{cfg: Config{APIKey: "test-key"}}
	calls := 0
	// context_length_exceeded triggers ShouldCompress, but no compressor → falls to backoff
	// Use context cancel to avoid slow waits
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = a.runWithRetry(ctx, func(cfg Config) error {
		calls++
		return errors.New("HTTP 400: bad request")
	})
	// 400 is non-retriable, should only call once even with default max 3
	if calls != 1 {
		t.Errorf("expected 1 call with non-retriable 400, got %d", calls)
	}
}
