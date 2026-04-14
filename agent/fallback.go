package agent

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/cloudwego/eino/components/model"
	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
)

// FallbackConfig 模型回退配置
type FallbackConfig struct {
	Provider string // 备用模型 provider (openai/claude)
	APIKey   string // 备用模型 API Key
	BaseURL  string // 备用模型 Base URL
	Model    string // 备用模型名称
}

// SetFallbackConfig 设置模型回退配置
func (a *Agent) SetFallbackConfig(cfg *FallbackConfig) {
	a.fallbackCfg = cfg
}

// createFallbackModel 创建回退模型
func (a *Agent) createFallbackModel(ctx context.Context) (model.ToolCallingChatModel, error) {
	if a.fallbackCfg == nil {
		return nil, fmt.Errorf("no fallback model configured")
	}

	fb := a.fallbackCfg
	apiKey := fb.APIKey
	if apiKey == "" {
		apiKey = a.cfg.APIKey // 复用主 Key
	}

	switch fb.Provider {
	case "claude":
		baseURL := fb.BaseURL
		var baseURLPtr *string
		if baseURL != "" {
			baseURLPtr = &baseURL
		}
		maxTokens := 4096
		if a.cfg.MaxTokens > 0 {
			maxTokens = a.cfg.MaxTokens
		}
		return claudemodel.NewChatModel(ctx, &claudemodel.Config{
			BaseURL:    baseURLPtr,
			APIKey:     apiKey,
			Model:      fb.Model,
			MaxTokens:  maxTokens,
			HTTPClient: &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		})
	default: // openai 兼容
		cfg := &openaimodel.ChatModelConfig{
			APIKey:      apiKey,
			BaseURL:     fb.BaseURL,
			Model:       fb.Model,
			Temperature: a.cfg.Temperature,
			HTTPClient:  &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		}
		if a.cfg.MaxTokens > 0 {
			cfg.MaxCompletionTokens = &a.cfg.MaxTokens
		}
		return openaimodel.NewChatModel(ctx, cfg)
	}
}

// shouldFallback 判断错误是否应该触发回退
func shouldFallback(err error) bool {
	classified := ClassifyAPIError(err)
	switch classified.Reason {
	case ReasonServerError, ReasonTimeout, ReasonRateLimit, ReasonBilling:
		return true
	default:
		return false
	}
}

// runWithFallback 主模型失败后尝试回退模型
// primaryErr 是主模型的错误，msgs 是原始消息
func (a *Agent) runWithFallback(ctx context.Context, primaryErr error, runFallback func(model.ToolCallingChatModel) error) error {
	if a.fallbackCfg == nil || !shouldFallback(primaryErr) {
		return primaryErr
	}

	log.Printf("[Fallback] 主模型失败 (%v)，切换到备用模型 %s/%s",
		primaryErr, a.fallbackCfg.Provider, a.fallbackCfg.Model)

	fbModel, err := a.createFallbackModel(ctx)
	if err != nil {
		log.Printf("[Fallback] 创建备用模型失败: %v", err)
		return primaryErr // 返回原始错误
	}

	if fbErr := runFallback(fbModel); fbErr != nil {
		log.Printf("[Fallback] 备用模型也失败: %v", fbErr)
		return fmt.Errorf("主模型: %w; 备用模型: %v", primaryErr, fbErr)
	}

	log.Printf("[Fallback] 备用模型执行成功")
	return nil
}
