package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(5, time.Second)

	// 前 5 个请求应被允许
	for i := 0; i < 5; i++ {
		if !rl.Allow("client-1") {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 第 6 个应被拒绝
	if rl.Allow("client-1") {
		t.Error("6th request should be rejected")
	}
}

func TestRateLimiterRefill(t *testing.T) {
	rl := NewRateLimiter(2, time.Second)

	// 用完令牌
	rl.Allow("client-1")
	rl.Allow("client-1")
	if rl.Allow("client-1") {
		t.Error("should be rate limited")
	}

	// 等待令牌恢复
	time.Sleep(600 * time.Millisecond)
	if !rl.Allow("client-1") {
		t.Error("token should have refilled")
	}
}

func TestRateLimiterDifferentClients(t *testing.T) {
	rl := NewRateLimiter(1, time.Second)

	if !rl.Allow("client-a") {
		t.Error("client-a first request should be allowed")
	}
	if !rl.Allow("client-b") {
		t.Error("client-b first request should be allowed (separate bucket)")
	}
	if rl.Allow("client-a") {
		t.Error("client-a second request should be rejected")
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := NewRateLimiter(2, time.Second)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 前 2 个应通过
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// 第 3 个应返回 429
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

func TestRateLimiterXForwardedFor(t *testing.T) {
	rl := NewRateLimiter(1, time.Second)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 同一 RemoteAddr 但不同 X-Forwarded-For
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "proxy:1234"
	req1.Header.Set("X-Forwarded-For", "10.0.0.1")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w1.Code)
	}

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "proxy:1234"
	req2.Header.Set("X-Forwarded-For", "10.0.0.2")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200 (different client via XFF), got %d", w2.Code)
	}
}
