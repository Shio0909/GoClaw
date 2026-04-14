package agent

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyAPIError_StatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		errMsg     string
		wantReason ErrorReason
		wantRetry  bool
		wantRotate bool
	}{
		{
			name:       "401 unauthorized",
			errMsg:     "HTTP 401: Unauthorized",
			wantReason: ReasonAuth,
			wantRetry:  false,
			wantRotate: true,
		},
		{
			name:       "402 billing",
			errMsg:     "HTTP 402: Payment Required",
			wantReason: ReasonBilling,
			wantRetry:  false,
			wantRotate: true,
		},
		{
			name:       "429 rate limit",
			errMsg:     "HTTP 429: Too Many Requests, retry after 30",
			wantReason: ReasonRateLimit,
			wantRetry:  true,
			wantRotate: true,
		},
		{
			name:       "500 server error",
			errMsg:     "HTTP 500: Internal Server Error",
			wantReason: ReasonServerError,
			wantRetry:  true,
			wantRotate: false,
		},
		{
			name:       "503 server error",
			errMsg:     "HTTP 503: Service Unavailable",
			wantReason: ReasonServerError,
			wantRetry:  true,
			wantRotate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := ClassifyAPIError(errors.New(tt.errMsg))
			if classified.Reason != tt.wantReason {
				t.Errorf("reason = %s, want %s", classified.Reason, tt.wantReason)
			}
			if classified.ShouldRetry != tt.wantRetry {
				t.Errorf("shouldRetry = %v, want %v", classified.ShouldRetry, tt.wantRetry)
			}
			if classified.ShouldRotateKey != tt.wantRotate {
				t.Errorf("shouldRotate = %v, want %v", classified.ShouldRotateKey, tt.wantRotate)
			}
		})
	}
}

func TestClassifyAPIError_MessagePatterns(t *testing.T) {
	tests := []struct {
		name       string
		errMsg     string
		wantReason ErrorReason
	}{
		{
			name:       "context overflow english",
			errMsg:     "context length exceeded: maximum 128000 tokens",
			wantReason: ReasonContextOverflow,
		},
		{
			name:       "context overflow chinese",
			errMsg:     "上下文太长了",
			wantReason: ReasonContextOverflow,
		},
		{
			name:       "timeout",
			errMsg:     "context deadline exceeded",
			wantReason: ReasonTimeout,
		},
		{
			name:       "invalid api key",
			errMsg:     "Invalid API key provided",
			wantReason: ReasonAuth,
		},
		{
			name:       "insufficient quota",
			errMsg:     "insufficient_quota: You have exceeded your billing limit",
			wantReason: ReasonBilling,
		},
		{
			name:       "unknown error",
			errMsg:     "something completely random happened",
			wantReason: ReasonUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classified := ClassifyAPIError(errors.New(tt.errMsg))
			if classified.Reason != tt.wantReason {
				t.Errorf("reason = %s, want %s", classified.Reason, tt.wantReason)
			}
		})
	}
}

func TestClassifyAPIError_Nil(t *testing.T) {
	classified := ClassifyAPIError(nil)
	if classified.Reason != "" {
		t.Errorf("nil error should have empty reason, got %s", classified.Reason)
	}
}

func TestClassifyAPIError_RetryAfter(t *testing.T) {
	classified := ClassifyAPIError(errors.New("HTTP 429: rate limited, retry after 45 seconds"))
	if classified.RetryAfterSeconds != 45 {
		t.Errorf("retryAfter = %d, want 45", classified.RetryAfterSeconds)
	}
}

func TestJitteredBackoff(t *testing.T) {
	// 第一次尝试：base = 3s
	d1 := JitteredBackoff(1)
	if d1 < 3*time.Second || d1 > 5*time.Second {
		t.Errorf("attempt 1 backoff = %v, want 3-5s", d1)
	}

	// 第二次尝试：base = 6s
	d2 := JitteredBackoff(2)
	if d2 < 6*time.Second || d2 > 10*time.Second {
		t.Errorf("attempt 2 backoff = %v, want 6-10s", d2)
	}

	// 第五次尝试应受 60s cap 限制
	d5 := JitteredBackoff(5)
	if d5 > 91*time.Second { // 60 + 60/2 = 90 max
		t.Errorf("attempt 5 backoff = %v, should be capped at ~90s", d5)
	}
}

func TestExtractStatusCode(t *testing.T) {
	tests := []struct {
		msg  string
		want int
	}{
		{"HTTP 429: Too Many Requests", 429},
		{"status: 503 service unavailable", 503},
		{"StatusCode:401 unauthorized", 401},
		{"random error with no code", 0},
	}
	for _, tt := range tests {
		got := extractStatusCode(tt.msg)
		if got != tt.want {
			t.Errorf("extractStatusCode(%q) = %d, want %d", tt.msg, got, tt.want)
		}
	}
}

// --- Credential Pool Tests ---

func TestCredentialPool_AddAndGet(t *testing.T) {
	pool := NewCredentialPool(StrategyFillFirst)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")
	pool.AddKey("key1", "openai", "") // duplicate

	if pool.Size() != 2 {
		t.Errorf("pool size = %d, want 2", pool.Size())
	}

	key, ok := pool.GetKey()
	if !ok || key != "key1" {
		t.Errorf("fill_first should return key1, got %s", key)
	}
}

func TestCredentialPool_RoundRobin(t *testing.T) {
	pool := NewCredentialPool(StrategyRoundRobin)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")
	pool.AddKey("key3", "openai", "")

	keys := make([]string, 4)
	for i := range keys {
		k, _ := pool.GetKey()
		keys[i] = k
	}

	// Should cycle: key1, key2, key3, key1
	if keys[0] != "key1" || keys[1] != "key2" || keys[2] != "key3" || keys[3] != "key1" {
		t.Errorf("round robin got %v, want [key1 key2 key3 key1]", keys)
	}
}

func TestCredentialPool_LeastUsed(t *testing.T) {
	pool := NewCredentialPool(StrategyLeastUsed)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")

	// Use key1 twice
	pool.GetKey() // key1 (use_count=1)
	pool.GetKey() // key2 (use_count=1) -- both equal, takes first

	// Now key1 has 1 use, key2 has 1 use; they're equal, so key1 is picked
	// Let's force key1 up
	pool.GetKey() // key1 again (use_count=2)

	// Now key2 should be least used
	k, _ := pool.GetKey()
	if k != "key2" {
		t.Errorf("least_used should pick key2, got %s", k)
	}
}

func TestCredentialPool_NextKey(t *testing.T) {
	pool := NewCredentialPool(StrategyRoundRobin)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")
	pool.AddKey("key3", "openai", "")

	next, ok := pool.NextKey("key1")
	if !ok || next == "key1" {
		t.Errorf("NextKey(key1) should return different key, got %s", next)
	}
}

func TestCredentialPool_DisabledKey(t *testing.T) {
	pool := NewCredentialPool(StrategyFillFirst)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")

	// Mark key1 as auth failure
	pool.RecordFailure("key1", ReasonAuth)

	key, ok := pool.GetKey()
	if !ok || key != "key2" {
		t.Errorf("should skip disabled key1, got %s", key)
	}
}

func TestCredentialPool_RateLimitRecovery(t *testing.T) {
	pool := NewCredentialPool(StrategyFillFirst)
	pool.AddKey("key1", "openai", "")
	pool.AddKey("key2", "openai", "")

	// Mark key1 as rate limited
	pool.RecordFailure("key1", ReasonRateLimit)

	// Should skip key1 (within cooldown)
	key, ok := pool.GetKey()
	if !ok || key != "key2" {
		t.Errorf("should skip rate-limited key1, got %s", key)
	}

	// Record success on key1 should clear rate limit
	pool.RecordSuccess("key1")

	if pool.AvailableCount() != 2 {
		t.Errorf("available count should be 2 after recovery, got %d", pool.AvailableCount())
	}
}

func TestCredentialPool_AllDisabled(t *testing.T) {
	pool := NewCredentialPool(StrategyFillFirst)
	pool.AddKey("key1", "openai", "")

	pool.RecordFailure("key1", ReasonAuth)

	_, ok := pool.GetKey()
	if ok {
		t.Error("should return false when all keys disabled")
	}

	_, ok = pool.NextKey("key1")
	if ok {
		t.Error("NextKey should return false when all keys disabled")
	}
}
