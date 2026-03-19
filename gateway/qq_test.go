package gateway

import (
	"strings"
	"testing"
)

func TestSplitMessageShort(t *testing.T) {
	chunks := splitMessage("短消息", 100)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != "短消息" {
		t.Fatalf("expected '短消息', got %s", chunks[0])
	}
}

func TestSplitMessageLong(t *testing.T) {
	// 构造一个超长消息
	msg := strings.Repeat("这是一段测试文字。", 200) // ~1800 字符
	chunks := splitMessage(msg, 500)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}

	// 验证所有 chunk 拼起来等于原文
	joined := strings.Join(chunks, "")
	if joined != msg {
		t.Fatal("chunks don't reconstruct original message")
	}

	// 验证每个 chunk 不超过限制
	for i, chunk := range chunks {
		runes := []rune(chunk)
		if len(runes) > 500 {
			t.Fatalf("chunk %d exceeds max length: %d runes", i, len(runes))
		}
	}
}

func TestSplitMessageAtNewline(t *testing.T) {
	msg := strings.Repeat("一二三四五\n", 120) // 每行6字符，共720字符
	chunks := splitMessage(msg, 100)

	// 应该在换行处分割
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 100 {
			t.Fatalf("chunk exceeds max: %d runes", len([]rune(chunk)))
		}
	}
}
