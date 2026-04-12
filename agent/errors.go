package agent

import (
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorReason 错误分类类型
type ErrorReason string

const (
	ReasonBilling         ErrorReason = "billing"          // 402: 额度不足
	ReasonRateLimit       ErrorReason = "rate_limit"       // 429: 速率限制
	ReasonAuth            ErrorReason = "auth"             // 401/403: 认证失败
	ReasonContextOverflow ErrorReason = "context_overflow" // 上下文溢出
	ReasonTimeout         ErrorReason = "timeout"          // 请求超时
	ReasonServerError     ErrorReason = "server_error"     // 500/502/503: 服务端错误
	ReasonUnknown         ErrorReason = "unknown"          // 未知错误
)

// ClassifiedError 分类后的错误，指导重试策略
type ClassifiedError struct {
	Reason             ErrorReason
	StatusCode         int
	Message            string
	ShouldRetry        bool
	ShouldCompress     bool // 上下文溢出时触发压缩
	ShouldRotateKey    bool // 应切换 API Key
	RetryAfterSeconds  int  // 服务端建议的等待时间
}

func (e ClassifiedError) Error() string {
	return fmt.Sprintf("[%s] HTTP %d: %s", e.Reason, e.StatusCode, e.Message)
}

// ClassifyAPIError 分析 API 错误并分类
func ClassifyAPIError(err error) ClassifiedError {
	if err == nil {
		return ClassifiedError{}
	}

	msg := err.Error()
	msgLower := strings.ToLower(msg)
	status := extractStatusCode(msg)

	// 按 HTTP 状态码分类
	switch status {
	case 401, 403:
		return ClassifiedError{
			Reason:          ReasonAuth,
			StatusCode:      status,
			Message:         msg,
			ShouldRetry:     false,
			ShouldRotateKey: true,
		}
	case 402:
		return ClassifiedError{
			Reason:          ReasonBilling,
			StatusCode:      status,
			Message:         msg,
			ShouldRetry:     false,
			ShouldRotateKey: true,
		}
	case 429:
		retryAfter := extractRetryAfter(msg)
		return ClassifiedError{
			Reason:            ReasonRateLimit,
			StatusCode:        status,
			Message:           msg,
			ShouldRetry:       true,
			RetryAfterSeconds: retryAfter,
		}
	case 500, 502, 503, 504:
		return ClassifiedError{
			Reason:      ReasonServerError,
			StatusCode:  status,
			Message:     msg,
			ShouldRetry: true,
		}
	}

	// 按错误消息内容分类
	if containsAny(msgLower, "context length", "context_length_exceeded", "too many tokens",
		"maximum context length", "token limit", "上下文", "context window") {
		return ClassifiedError{
			Reason:         ReasonContextOverflow,
			StatusCode:     status,
			Message:        msg,
			ShouldRetry:    true,
			ShouldCompress: true,
		}
	}

	if containsAny(msgLower, "rate limit", "rate_limit", "too many requests", "请求过多", "限流") {
		return ClassifiedError{
			Reason:      ReasonRateLimit,
			StatusCode:  429,
			Message:     msg,
			ShouldRetry: true,
		}
	}

	if containsAny(msgLower, "timeout", "timed out", "deadline exceeded", "超时") {
		return ClassifiedError{
			Reason:      ReasonTimeout,
			StatusCode:  status,
			Message:     msg,
			ShouldRetry: true,
		}
	}

	if containsAny(msgLower, "unauthorized", "invalid api key", "invalid_api_key",
		"authentication", "认证失败", "invalid key", "api key") {
		return ClassifiedError{
			Reason:          ReasonAuth,
			StatusCode:      status,
			Message:         msg,
			ShouldRetry:     false,
			ShouldRotateKey: true,
		}
	}

	if containsAny(msgLower, "insufficient", "billing", "quota", "balance",
		"余额不足", "额度", "insufficient_quota") {
		return ClassifiedError{
			Reason:          ReasonBilling,
			StatusCode:      status,
			Message:         msg,
			ShouldRetry:     false,
			ShouldRotateKey: true,
		}
	}

	// 默认：未知错误，尝试重试
	return ClassifiedError{
		Reason:      ReasonUnknown,
		StatusCode:  status,
		Message:     msg,
		ShouldRetry: true,
	}
}

// JitteredBackoff 计算带抖动的指数退避等待时间
func JitteredBackoff(attempt int) time.Duration {
	exp := attempt - 1
	if exp < 0 {
		exp = 0
	}
	base := 3 << exp // 3, 6, 12, 24, 48, ...
	if base > 60 {
		base = 60
	}
	// 添加 [0, base/2] 的随机抖动
	jitter := rand.Intn(base/2 + 1)
	return time.Duration(base+jitter) * time.Second
}

// ParseRateLimitHeaders 解析速率限制响应头
func ParseRateLimitHeaders(headers http.Header) (remaining int, resetSeconds float64) {
	if r := headers.Get("x-ratelimit-remaining-requests"); r != "" {
		remaining, _ = strconv.Atoi(r)
	}
	if r := headers.Get("retry-after"); r != "" {
		resetSeconds, _ = strconv.ParseFloat(r, 64)
	} else if r := headers.Get("x-ratelimit-reset-requests"); r != "" {
		resetSeconds, _ = strconv.ParseFloat(r, 64)
	}
	return
}

// --- 内部工具函数 ---

func extractStatusCode(msg string) int {
	// 尝试从 "HTTP 429" 或 "status: 429" 或 "status_code: 429" 格式提取
	patterns := []string{"HTTP ", "status: ", "status_code: ", "StatusCode:", "http_status_code\":"}
	for _, p := range patterns {
		if idx := strings.Index(msg, p); idx >= 0 {
			numStr := msg[idx+len(p):]
			// 取前 3 个字符作为状态码
			if len(numStr) >= 3 {
				if code, err := strconv.Atoi(numStr[:3]); err == nil && code >= 100 && code < 600 {
					return code
				}
			}
		}
	}
	return 0
}

func extractRetryAfter(msg string) int {
	// 尝试提取 "retry after X" 或 "Retry-After: X"
	lower := strings.ToLower(msg)
	patterns := []string{"retry after ", "retry-after: ", "retry_after\":"}
	for _, p := range patterns {
		if idx := strings.Index(lower, p); idx >= 0 {
			numStr := msg[idx+len(p):]
			var seconds int
			for _, ch := range numStr {
				if ch >= '0' && ch <= '9' {
					seconds = seconds*10 + int(ch-'0')
				} else {
					break
				}
			}
			if seconds > 0 {
				return seconds
			}
		}
	}
	return 0
}

func containsAny(s string, patterns ...string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
