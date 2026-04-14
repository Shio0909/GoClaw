package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	toolRetryMax    = 3
	toolRetryBaseMs = 500
)

// isTransientError 判断是否为可重试的瞬时错误（网络超时、连接重置等）
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, p := range []string{
		"connection reset", "connection refused", "eof",
		"broken pipe", "timeout", "i/o timeout",
		"tls handshake", "server closed",
		"503", "502", "429",
	} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// maxToolResultBytes 工具结果最大字节数，超出则截断
var maxToolResultBytes = 30 * 1024 // 30KB, can be overridden via config

// SetMaxToolResultBytes 设置工具结果最大字节数（供 config 调用）
func SetMaxToolResultBytes(n int) {
	if n > 0 {
		maxToolResultBytes = n
	}
}

// EinoTool 将 ToolDef 适配为 Eino 的 InvokableTool 接口
type EinoTool struct {
	def             *ToolDef
	disabledChecker DisabledChecker
}

// NewEinoTool 包装一个 ToolDef 为 Eino InvokableTool
func NewEinoTool(def *ToolDef) tool.InvokableTool {
	return &EinoTool{def: def}
}

// NewEinoToolWithChecker 包装一个 ToolDef 并附加运行时禁用检查
func NewEinoToolWithChecker(def *ToolDef, checker DisabledChecker) tool.InvokableTool {
	return &EinoTool{def: def, disabledChecker: checker}
}

// Info 返回工具的元信息（名称、描述、参数 schema）
func (t *EinoTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	params := make(map[string]*schema.ParameterInfo)
	for _, p := range t.def.Parameters {
		params[p.Name] = &schema.ParameterInfo{
			Type:     toSchemaType(p.Type),
			Desc:     p.Description,
			Required: p.Required,
		}
	}

	info := &schema.ToolInfo{
		Name: t.def.Name,
		Desc: t.def.Description,
	}
	if len(params) > 0 {
		info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	}
	return info, nil
}

// InvokableRun 执行工具，接收 JSON 参数字符串，返回结果字符串
func (t *EinoTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	// 运行时禁用检查
	if t.disabledChecker != nil && t.disabledChecker(t.def.Name) {
		return fmt.Sprintf("Tool %q is currently disabled by the administrator.", t.def.Name), nil
	}

	var args map[string]interface{}
	if argumentsInJSON != "" && argumentsInJSON != "{}" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", err
		}
	}
	if args == nil {
		args = make(map[string]interface{})
	}

	// 危险工具确认（CLI 模式下提示用户）
	if err := requestConfirmation(ctx, t.def.Name, args); err != nil {
		return err.Error(), nil // return as tool result, not error
	}

	// 执行工具（可重试工具遇到瞬时错误时自动重试）
	var result string
	var err error
	maxAttempts := 1
	if t.def.Retryable {
		maxAttempts = toolRetryMax
	}
	start := time.Now()
	retries := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err = t.def.Fn(ctx, args)
		if err == nil || !t.def.Retryable || !isTransientError(err) {
			break
		}
		retries++
		if attempt < maxAttempts {
			wait := time.Duration(toolRetryBaseMs*(1<<(attempt-1))) * time.Millisecond
			log.Printf("[Tool] %s 瞬时错误 (尝试 %d/%d): %v — %v 后重试", t.def.Name, attempt, maxAttempts, err, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	duration := time.Since(start)
	globalStats.RecordCall(t.def.Name, duration, err, retries)

	// 头尾保留截断策略：保留开头和结尾，中间插入截断提示
	if len(result) > maxToolResultBytes {
		log.Printf("[Tool] %s 结果过大 (%d 字节)，截断到 %d 字节", t.def.Name, len(result), maxToolResultBytes)
		runes := []rune(result)
		totalRuneBytes := len(result)
		if totalRuneBytes > maxToolResultBytes {
			// 头部占 40%，尾部占 40%，留 20% 给截断提示
			headBudget := maxToolResultBytes * 40 / 100
			tailBudget := maxToolResultBytes * 40 / 100

			// 按 rune 边界找头部截止点
			headCut := 0
			headSize := 0
			for i, r := range runes {
				headSize += len(string(r))
				if headSize > headBudget {
					headCut = i
					break
				}
			}

			// 按 rune 边界找尾部起始点
			tailStart := len(runes)
			tailSize := 0
			for i := len(runes) - 1; i >= 0; i-- {
				tailSize += len(string(runes[i]))
				if tailSize > tailBudget {
					tailStart = i + 1
					break
				}
			}

			if headCut > 0 && tailStart < len(runes) {
				omitted := totalRuneBytes - headSize - tailSize
				result = string(runes[:headCut]) +
					fmt.Sprintf("\n\n... [省略 %d 字节 / 原始大小: %d 字节] ...\n\n", omitted, totalRuneBytes) +
					string(runes[tailStart:])
			}
		}
	}

	return result, err
}

// toSchemaType 将字符串类型映射为 Eino schema.DataType
func toSchemaType(t string) schema.DataType {
	switch t {
	case "number":
		return schema.Number
	case "integer":
		return schema.Integer
	case "boolean":
		return schema.Boolean
	case "array":
		return schema.Array
	default:
		return schema.String
	}
}

// ToEinoTools 将 Registry 中所有工具转换为 Eino InvokableTool 列表
func (r *Registry) ToEinoTools() []tool.InvokableTool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]tool.InvokableTool, 0, len(r.tools))
	for _, def := range r.tools {
		if r.disabledChecker != nil {
			result = append(result, NewEinoToolWithChecker(def, r.disabledChecker))
		} else {
			result = append(result, NewEinoTool(def))
		}
	}
	return result
}
