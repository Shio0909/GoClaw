package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestEstimateStringTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		minToken int
		maxToken int
	}{
		{"empty", "", 0, 0},
		{"ascii short", "hello", 1, 3},
		{"ascii sentence", "The quick brown fox jumps over the lazy dog", 8, 15},
		{"chinese short", "你好世界", 3, 5},
		{"chinese sentence", "这是一段中文测试文本，用来验证 CJK 的估算", 15, 30},
		{"mixed", "Hello 你好 World 世界", 4, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimateStringTokens(tt.input)
			if tokens < tt.minToken || tokens > tt.maxToken {
				t.Errorf("estimateStringTokens(%q) = %d, expected between %d and %d",
					tt.input, tokens, tt.minToken, tt.maxToken)
			}
		})
	}
}

func TestEstimateTokens_CJKAware(t *testing.T) {
	// CJK text should yield MORE tokens per character than pure ASCII
	asciiMsg := []*schema.Message{{Role: schema.User, Content: "aaaa"}} // 4 ASCII chars
	cjkMsg := []*schema.Message{{Role: schema.User, Content: "你好世界"}}   // 4 CJK chars

	asciiTokens := estimateTokens(asciiMsg)
	cjkTokens := estimateTokens(cjkMsg)

	// CJK should estimate more tokens than the same number of ASCII chars
	if cjkTokens <= asciiTokens {
		t.Errorf("CJK tokens (%d) should be >= ASCII tokens (%d) for same char count", cjkTokens, asciiTokens)
	}
}
