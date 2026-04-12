package agent

import (
	"log"
	"strings"
	"unicode/utf8"
)

// Complexity 请求复杂度
type Complexity int

const (
	ComplexitySimple  Complexity = iota // 简单问题（闲聊、翻译、查询）
	ComplexityComplex                   // 复杂问题（分析、代码、多步骤）
)

// ModelRoute 路由结果
type ModelRoute struct {
	Model      string
	Provider   string // 可选：不同复杂度用不同 provider
	BaseURL    string // 可选
	Complexity Complexity
}

// RouterConfig 路由配置
type RouterConfig struct {
	SimpleModel  string // 简单问题使用的模型
	ComplexModel string // 复杂问题使用的模型

	// 可选：不同模型使用不同 provider/baseURL
	SimpleProvider string
	SimpleBaseURL  string
	ComplexProvider string
	ComplexBaseURL  string
}

// ModelRouter 智能模型路由器
type ModelRouter struct {
	cfg RouterConfig
}

// NewModelRouter 创建路由器
func NewModelRouter(cfg RouterConfig) *ModelRouter {
	return &ModelRouter{cfg: cfg}
}

// Route 根据输入复杂度选择模型
func (r *ModelRouter) Route(input string) ModelRoute {
	complexity := classifyComplexity(input)

	var route ModelRoute
	route.Complexity = complexity

	switch complexity {
	case ComplexitySimple:
		route.Model = r.cfg.SimpleModel
		route.Provider = r.cfg.SimpleProvider
		route.BaseURL = r.cfg.SimpleBaseURL
	case ComplexityComplex:
		route.Model = r.cfg.ComplexModel
		route.Provider = r.cfg.ComplexProvider
		route.BaseURL = r.cfg.ComplexBaseURL
	}

	log.Printf("[Router] 复杂度=%s → 模型=%s", complexityName(complexity), route.Model)
	return route
}

// classifyComplexity 基于启发式规则判断输入复杂度
func classifyComplexity(input string) Complexity {
	inputLen := utf8.RuneCountInString(input)
	lower := strings.ToLower(input)

	// 长文本通常更复杂
	if inputLen > 300 {
		return ComplexityComplex
	}

	// 包含代码块
	if strings.Contains(input, "```") || strings.Contains(input, "func ") ||
		strings.Contains(input, "def ") || strings.Contains(input, "class ") {
		return ComplexityComplex
	}

	// 复杂意图关键词
	complexKeywords := []string{
		"分析", "比较", "对比", "设计", "架构", "重构", "优化",
		"实现", "编写", "写一个", "写个", "帮我写", "生成代码",
		"debug", "调试", "排查", "为什么", "原因",
		"总结", "归纳", "评估", "review",
		"多步", "步骤", "流程", "方案",
		"analyze", "compare", "implement", "refactor", "design",
		"explain how", "write a", "create a", "build a",
	}
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityComplex
		}
	}

	// 多行输入通常更复杂
	lineCount := strings.Count(input, "\n") + 1
	if lineCount > 3 {
		return ComplexityComplex
	}

	// 包含多个问号（多问题）
	if strings.Count(input, "?") + strings.Count(input, "？") > 1 {
		return ComplexityComplex
	}

	// 简单意图关键词
	simpleKeywords := []string{
		"你好", "hi", "hello", "嗨", "在吗", "你是谁",
		"翻译", "translate",
		"什么是", "是什么", "什么意思",
		"几点", "天气", "日期",
		"谢谢", "感谢", "好的", "ok", "bye", "再见",
	}
	for _, kw := range simpleKeywords {
		if strings.Contains(lower, kw) {
			return ComplexitySimple
		}
	}

	// 短输入默认简单
	if inputLen < 30 {
		return ComplexitySimple
	}

	// 默认复杂
	return ComplexityComplex
}

func complexityName(c Complexity) string {
	switch c {
	case ComplexitySimple:
		return "simple"
	case ComplexityComplex:
		return "complex"
	default:
		return "unknown"
	}
}
