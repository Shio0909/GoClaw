package agent

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RetryConfig 重试配置
type RetryConfig struct {
	MaxAttempts int            // 最大重试次数（含首次），默认 3
	Pool        *CredentialPool // 凭证池（可选）
}

// runWithRetry 带重试和 Key 轮换的执行包装
// fn 接收当前 agent config，返回错误。调用者负责使用 config 中的 key。
func (a *Agent) runWithRetry(ctx context.Context, fn func(cfg Config) error) error {
	maxAttempts := 3
	if a.retryConfig != nil && a.retryConfig.MaxAttempts > 0 {
		maxAttempts = a.retryConfig.MaxAttempts
	}

	cfg := a.cfg
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := fn(cfg)
		if err == nil {
			// 成功：如果用了池中的 key，记录成功
			if a.retryConfig != nil && a.retryConfig.Pool != nil {
				a.retryConfig.Pool.RecordSuccess(cfg.APIKey)
			}
			return nil
		}

		classified := ClassifyAPIError(err)
		log.Printf("[Retry] 第 %d/%d 次失败 [%s]: %s", attempt, maxAttempts, classified.Reason, classified.Message)

		// 不可重试的错误，直接返回
		if !classified.ShouldRetry && !classified.ShouldRotateKey {
			return err
		}

		// 最后一次尝试，不再重试
		if attempt >= maxAttempts {
			return fmt.Errorf("重试 %d 次后仍然失败: %w", maxAttempts, err)
		}

		// 需要轮换 Key
		if classified.ShouldRotateKey && a.retryConfig != nil && a.retryConfig.Pool != nil {
			pool := a.retryConfig.Pool
			pool.RecordFailure(cfg.APIKey, classified.Reason)
			if nextKey, ok := pool.NextKey(cfg.APIKey); ok {
				log.Printf("[Retry] 切换 Key: ...%s → ...%s", maskKey(cfg.APIKey), maskKey(nextKey))
				cfg.APIKey = nextKey
				continue // 立即用新 key 重试，不等待
			}
			log.Printf("[Retry] 无可用备选 Key")
			return fmt.Errorf("所有 API Key 均不可用: %w", err)
		}

		// 需要压缩上下文
		if classified.ShouldCompress && a.compressor != nil {
			log.Printf("[Retry] 上下文溢出，触发压缩...")
			a.history = a.compressor.ForceCompress(ctx, a.history)
			continue // 压缩后立即重试
		}

		// 计算等待时间
		var wait time.Duration
		if classified.RetryAfterSeconds > 0 {
			wait = time.Duration(classified.RetryAfterSeconds) * time.Second
		} else {
			wait = JitteredBackoff(attempt)
		}

		log.Printf("[Retry] 等待 %.1f 秒后重试...", wait.Seconds())
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil // unreachable
}

// maskKey 遮蔽 API Key，仅显示末尾 4 位
func maskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[len(key)-4:]
}
