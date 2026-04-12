package agent

import (
	"testing"
)

func TestClassifyComplexity_Simple(t *testing.T) {
	simpleInputs := []string{
		"你好",
		"hi",
		"什么是 Go",
		"翻译一下 hello world",
		"今天几点了",
		"谢谢",
		"ok",
		"嗨",
	}
	for _, input := range simpleInputs {
		c := classifyComplexity(input)
		if c != ComplexitySimple {
			t.Errorf("classifyComplexity(%q) = %d, want Simple(0)", input, c)
		}
	}
}

func TestClassifyComplexity_Complex(t *testing.T) {
	complexInputs := []string{
		"帮我分析一下这段代码的性能问题",
		"比较 React 和 Vue 的优缺点",
		"请实现一个二叉树的遍历算法",
		"帮我写一个 HTTP 服务器",
		"为什么 goroutine 会泄漏",
		"```go\nfunc main() {\n}\n```",
		"这是一段很长的文本，" + string(make([]rune, 350)),
		"第一个问题？第二个问题？",
		"这个项目的\n架构设计\n应该怎么\n改进呢",
	}
	for _, input := range complexInputs {
		c := classifyComplexity(input)
		if c != ComplexityComplex {
			t.Errorf("classifyComplexity(%q...) = %d, want Complex(1)", truncateTestInput(input), c)
		}
	}
}

func TestModelRouter_Route(t *testing.T) {
	router := NewModelRouter(RouterConfig{
		SimpleModel:  "gpt-4o-mini",
		ComplexModel: "gpt-4o",
	})

	// 简单问题
	route := router.Route("你好")
	if route.Model != "gpt-4o-mini" {
		t.Errorf("simple route model = %s, want gpt-4o-mini", route.Model)
	}
	if route.Complexity != ComplexitySimple {
		t.Errorf("simple route complexity = %d, want Simple", route.Complexity)
	}

	// 复杂问题
	route = router.Route("帮我实现一个缓存系统")
	if route.Model != "gpt-4o" {
		t.Errorf("complex route model = %s, want gpt-4o", route.Model)
	}
	if route.Complexity != ComplexityComplex {
		t.Errorf("complex route complexity = %d, want Complex", route.Complexity)
	}
}

func TestModelRouter_RouteWithProvider(t *testing.T) {
	router := NewModelRouter(RouterConfig{
		SimpleModel:    "MiniMax-M2.5",
		ComplexModel:   "claude-sonnet",
		SimpleProvider: "minimax",
		ComplexProvider: "claude",
		SimpleBaseURL:  "https://api.minimax.chat/v1",
		ComplexBaseURL: "https://api.anthropic.com",
	})

	route := router.Route("翻译 hello")
	if route.Provider != "minimax" {
		t.Errorf("simple provider = %s, want minimax", route.Provider)
	}

	route = router.Route("帮我重构这个模块的架构")
	if route.Provider != "claude" {
		t.Errorf("complex provider = %s, want claude", route.Provider)
	}
}

func truncateTestInput(s string) string {
	runes := []rune(s)
	if len(runes) > 30 {
		return string(runes[:30]) + "..."
	}
	return s
}
