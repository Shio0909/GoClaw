package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type CapabilityToolConfig struct {
	Endpoint string
	APIKey   string
	Timeout  time.Duration
	Stub     bool
}

type capabilityRequest struct {
	Capability string      `json:"capability"`
	Input      interface{} `json:"input,omitempty"`
}

func NewInvokeCapabilityTool(cfg CapabilityToolConfig) *ToolDef {
	return &ToolDef{
		Name:        "invoke_capability",
		Description: "调用已配置的后端能力路由器。用于按需访问知识库、IM、记忆等高层能力，避免暴露大量底层工具。",
		Parameters: []ParamDef{
			{Name: "capability", Type: "string", Description: "能力名称，如 rag.search、rag.read_wiki、kb.import_url", Required: true},
			{Name: "input", Type: "string", Description: "能力输入，建议使用 JSON 字符串；也可传普通文本", Required: false},
		},
		Retryable: true,
		Timeout:   cfg.Timeout,
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			capability, _ := args["capability"].(string)
			capability = strings.TrimSpace(capability)
			if capability == "" {
				return "", fmt.Errorf("capability is required")
			}

			input := args["input"]
			if text, ok := input.(string); ok {
				input = parseCapabilityInput(text)
			}

			if cfg.Stub || strings.TrimSpace(cfg.Endpoint) == "" {
				return stubCapabilityResult(capability, input)
			}
			return callCapabilityBackend(ctx, cfg, capability, input)
		},
	}
}

func parseCapabilityInput(text string) interface{} {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value
	}
	return text
}

func stubCapabilityResult(capability string, input interface{}) (string, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"capability": capability,
		"input":      input,
		"stub":       true,
		"message":    "capability backend is not configured",
	})
	return string(payload), nil
}

func callCapabilityBackend(ctx context.Context, cfg CapabilityToolConfig, capability string, input interface{}) (string, error) {
	body, err := json.Marshal(capabilityRequest{Capability: capability, Input: input})
	if err != nil {
		return "", fmt.Errorf("marshal capability request: %w", err)
	}

	client := &http.Client{Timeout: cfg.Timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create capability request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("call capability backend: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("capability backend HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return string(respBody), nil
}
