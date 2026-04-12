package gateway

import (
	"strings"
	"sync"
	"testing"
	"time"
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

// -------- 去重器测试 --------

func TestDedupRingBasic(t *testing.T) {
	d := newDedupRing(10)

	// 第一次见到应返回 false
	if d.seen(100) {
		t.Fatal("first time should not be seen")
	}
	// 第二次应返回 true
	if !d.seen(100) {
		t.Fatal("second time should be seen")
	}
	// 不同 ID 应返回 false
	if d.seen(200) {
		t.Fatal("different ID should not be seen")
	}
}

func TestDedupRingWraparound(t *testing.T) {
	d := newDedupRing(5)

	// 填满缓冲区
	for i := int64(1); i <= 5; i++ {
		d.seen(i)
	}

	// 所有应该都能找到
	for i := int64(1); i <= 5; i++ {
		if !d.seen(i) {
			t.Fatalf("ID %d should be seen", i)
		}
	}

	// 添加新的会覆盖最老的
	d.seen(6)
	// ID 1 应该被覆盖了
	if d.seen(1) {
		t.Fatal("ID 1 should have been evicted")
	}
}

func TestDedupRingConcurrency(t *testing.T) {
	d := newDedupRing(100)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(base int64) {
			defer wg.Done()
			for j := int64(0); j < 20; j++ {
				d.seen(base*100 + j)
			}
		}(int64(i))
	}
	wg.Wait()
}

func TestDedupRingZeroID(t *testing.T) {
	d := newDedupRing(10)
	// msgID 0 不应触发去重
	if d.seen(0) {
		t.Fatal("zero ID should not match empty entries")
	}
}

// -------- 发送限流器测试 --------

func TestSendLimiterPacing(t *testing.T) {
	l := newSendLimiter(50 * time.Millisecond)

	start := time.Now()
	l.wait()
	l.wait()
	l.wait()
	elapsed := time.Since(start)

	// 3次调用应至少花 100ms（第一次不等待，后两次各等 50ms）
	if elapsed < 80*time.Millisecond {
		t.Fatalf("expected >= 80ms, got %v", elapsed)
	}
}
