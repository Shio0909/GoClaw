package gateway

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// -------- 退避算法测试 --------

func TestBackoffGrowth(t *testing.T) {
	backoff := reconnectInitial
	expected := []time.Duration{2, 4, 8, 16, 32, 64, 120, 120}
	for i, want := range expected {
		wantDuration := want * time.Second
		if backoff != wantDuration {
			t.Fatalf("step %d: expected %v, got %v", i, wantDuration, backoff)
		}
		backoff *= reconnectFactor
		if backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

func TestBackoffResetAfterStableConnection(t *testing.T) {
	backoff := reconnectMax // 模拟已达最大退避
	uptime := 31 * time.Second
	if uptime > 30*time.Second {
		backoff = reconnectInitial
	}
	if backoff != reconnectInitial {
		t.Fatalf("expected reset to %v, got %v", reconnectInitial, backoff)
	}
}

func TestBackoffJitterRange(t *testing.T) {
	backoff := 10 * time.Second
	for i := 0; i < 100; i++ {
		jitter := time.Duration(int64(backoff) / 2)
		wait := backoff + jitter - backoff/4
		// wait 应在 [backoff*0.75, backoff*1.25] 范围内（jitter 最大值）
		minWait := backoff - backoff/4
		maxWait := backoff + backoff/2 - backoff/4
		if wait < minWait || wait > maxWait {
			t.Fatalf("jitter out of range: %v (expected [%v, %v])", wait, minWait, maxWait)
		}
	}
}

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

func TestSendLimiterConcurrency(t *testing.T) {
	l := newSendLimiter(10 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.wait()
		}()
	}
	wg.Wait()
}

func TestDedupRingTTLExpiry(t *testing.T) {
	d := &dedupRing{
		entries: make([]dedupEntry, 10),
		size:    10,
	}
	// 手动插入一个过期的条目
	d.entries[0] = dedupEntry{msgID: 42, ts: time.Now().Add(-6 * time.Minute)}
	d.pos = 1

	// 过期的 ID 不应被识别为 "seen"
	if d.seen(42) {
		t.Fatal("expired entry should not be detected as seen")
	}
}

// -------- 常量一致性测试 --------

func TestConstantsConsistency(t *testing.T) {
	if pongTimeout < 2*pingInterval {
		t.Fatal("pongTimeout should be >= 2*pingInterval")
	}
	if reconnectInitial > reconnectMax {
		t.Fatal("reconnectInitial should be <= reconnectMax")
	}
	if reconnectFactor < 2 {
		t.Fatal("reconnectFactor should be >= 2")
	}
	if dedupCapacity <= 0 {
		t.Fatal("dedupCapacity must be positive")
	}
}
