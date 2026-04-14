package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	stats := m.Stats()
	if stats["hooks"].(int) != 0 {
		t.Fatal("expected 0 hooks")
	}
}

func TestEmitDelivery(t *testing.T) {
	var received atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		body, _ := io.ReadAll(r.Body)
		var payload Payload
		json.Unmarshal(body, &payload)
		if payload.Event != EventChatComplete {
			t.Errorf("expected chat.complete, got %s", payload.Event)
		}
		if payload.Session != "s1" {
			t.Errorf("expected session=s1, got %s", payload.Session)
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	m := NewManager([]Hook{
		{URL: ts.URL, Events: []EventType{EventChatComplete}},
	})
	defer m.Close()

	m.Emit(EventChatComplete, "s1", map[string]string{"model": "gpt-4o"})

	// 等待异步投递
	time.Sleep(200 * time.Millisecond)

	if received.Load() != 1 {
		t.Fatalf("expected 1 delivery, got %d", received.Load())
	}
	if m.sent.Load() != 1 {
		t.Fatalf("sent count: want 1, got %d", m.sent.Load())
	}
}

func TestEmitEventFilter(t *testing.T) {
	var received atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	m := NewManager([]Hook{
		{URL: ts.URL, Events: []EventType{EventChatComplete}},
	})
	defer m.Close()

	// 发送不匹配的事件
	m.Emit(EventToolCall, "s1", nil)

	time.Sleep(100 * time.Millisecond)

	if received.Load() != 0 {
		t.Fatalf("should not deliver filtered event, got %d", received.Load())
	}
}

func TestEmitAllEvents(t *testing.T) {
	var received atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(200)
	}))
	defer ts.Close()

	// 空 Events 列表 = 订阅全部
	m := NewManager([]Hook{
		{URL: ts.URL},
	})
	defer m.Close()

	m.Emit(EventChatComplete, "s1", nil)
	m.Emit(EventToolCall, "s1", nil)
	m.Emit(EventChatError, "s1", nil)

	time.Sleep(300 * time.Millisecond)

	if received.Load() != 3 {
		t.Fatalf("expected 3 deliveries, got %d", received.Load())
	}
}

func TestHMACSignature(t *testing.T) {
	secret := "test-secret-key"
	var receivedSig string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-GoClaw-Signature")
		w.WriteHeader(200)
	}))
	defer ts.Close()

	m := NewManager([]Hook{
		{URL: ts.URL, Secret: secret},
	})
	defer m.Close()

	m.Emit(EventChatComplete, "s1", nil)
	time.Sleep(200 * time.Millisecond)

	if receivedSig == "" {
		t.Fatal("expected X-GoClaw-Signature header")
	}
	if len(receivedSig) < 10 {
		t.Fatal("signature too short")
	}
	// 验证签名格式
	if receivedSig[:7] != "sha256=" {
		t.Fatalf("expected sha256= prefix, got %s", receivedSig[:7])
	}
}

func TestSignFunction(t *testing.T) {
	payload := []byte(`{"event":"test"}`)
	secret := "my-secret"

	sig := sign(payload, secret)

	// 独立计算验证
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))

	if sig != expected {
		t.Fatalf("signature mismatch:\n  got: %s\n  want: %s", sig, expected)
	}
}

func TestAddRemoveHook(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	m.AddHook(Hook{URL: "http://a.com"})
	m.AddHook(Hook{URL: "http://b.com"})

	hooks := m.ListHooks()
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}

	removed := m.RemoveHook("http://a.com")
	if !removed {
		t.Fatal("expected removal to succeed")
	}

	hooks = m.ListHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook after removal, got %d", len(hooks))
	}
	if hooks[0]["url"] != "http://b.com" {
		t.Fatalf("wrong hook remaining: %v", hooks[0]["url"])
	}
}

func TestRemoveNonexistent(t *testing.T) {
	m := NewManager(nil)
	defer m.Close()

	if m.RemoveHook("http://nonexistent.com") {
		t.Fatal("should not remove nonexistent hook")
	}
}

func TestListHooksSecretMasked(t *testing.T) {
	m := NewManager([]Hook{
		{URL: "http://a.com", Secret: "super-secret"},
	})
	defer m.Close()

	hooks := m.ListHooks()
	if hooks[0]["has_secret"] != true {
		t.Fatal("expected has_secret=true")
	}
	// secret should NOT appear in the output
	if _, hasSecret := hooks[0]["secret"]; hasSecret {
		t.Fatal("secret should not be in listed hooks")
	}
}

func TestFailedDeliveryCount(t *testing.T) {
	// 使用一个不存在的端口
	m := NewManager([]Hook{
		{URL: "http://127.0.0.1:1"},
	})
	defer m.Close()

	m.Emit(EventChatComplete, "s1", nil)
	time.Sleep(300 * time.Millisecond)

	if m.failed.Load() != 1 {
		t.Fatalf("expected 1 failed delivery, got %d", m.failed.Load())
	}
}

func TestStatsOutput(t *testing.T) {
	m := NewManager([]Hook{
		{URL: "http://a.com"},
		{URL: "http://b.com"},
	})
	defer m.Close()

	stats := m.Stats()
	if stats["hooks"].(int) != 2 {
		t.Fatalf("expected 2 hooks, got %v", stats["hooks"])
	}
	if stats["sent"].(int64) != 0 {
		t.Fatal("expected 0 sent")
	}
}
