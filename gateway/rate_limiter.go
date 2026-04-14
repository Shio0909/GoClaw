package gateway

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter 基于令牌桶的速率限制器
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	rate     int           // 每时间窗口允许的请求数
	window   time.Duration // 时间窗口
	cleanTTL time.Duration // 桶过期清理时间
}

type tokenBucket struct {
	tokens    float64
	maxTokens float64
	refillRate float64 // tokens/秒
	lastRefill time.Time
}

// NewRateLimiter 创建速率限制器
// rate: 每窗口允许的请求数, window: 时间窗口
func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*tokenBucket),
		rate:     rate,
		window:   window,
		cleanTTL: window * 10,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) getBucket(key string) *tokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		b = &tokenBucket{
			tokens:     float64(rl.rate),
			maxTokens:  float64(rl.rate),
			refillRate: float64(rl.rate) / rl.window.Seconds(),
			lastRefill: time.Now(),
		}
		rl.buckets[key] = b
	}
	return b
}

// Allow 检查是否允许请求（消耗一个令牌）
func (rl *RateLimiter) Allow(key string) bool {
	b := rl.getBucket(key)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Middleware 返回 HTTP 中间件
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			key = fwd
		}

		if !rl.Allow(key) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanTTL)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for key, b := range rl.buckets {
			if now.Sub(b.lastRefill) > rl.cleanTTL {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}
