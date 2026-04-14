package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// EventType webhook 事件类型
type EventType string

const (
	EventChatComplete EventType = "chat.complete"
	EventChatError    EventType = "chat.error"
	EventToolCall     EventType = "tool.call"
	EventSessionCreate EventType = "session.create"
	EventSessionDelete EventType = "session.delete"
)

// Payload webhook 发送的载荷
type Payload struct {
	Event     EventType         `json:"event"`
	Timestamp time.Time         `json:"timestamp"`
	Session   string            `json:"session,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
}

// Hook 单个 webhook 配置
type Hook struct {
	URL    string      `json:"url"`
	Secret string      `json:"secret,omitempty"` // HMAC-SHA256 签名密钥
	Events []EventType `json:"events"`           // 订阅的事件类型（空 = 全部）
}

// Manager webhook 管理器
type Manager struct {
	mu      sync.RWMutex
	hooks   []Hook
	client  *http.Client
	sent    atomic.Int64
	failed  atomic.Int64
	queue   chan delivery
	done    chan struct{}
}

type delivery struct {
	hook    Hook
	payload []byte
	sig     string
}

// NewManager 创建 webhook 管理器
func NewManager(hooks []Hook) *Manager {
	m := &Manager{
		hooks: hooks,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue: make(chan delivery, 256),
		done:  make(chan struct{}),
	}

	// 后台 worker 异步发送 webhook
	go m.worker()

	return m
}

func (m *Manager) worker() {
	for {
		select {
		case d := <-m.queue:
			m.send(d)
		case <-m.done:
			// 排空队列
			for {
				select {
				case d := <-m.queue:
					m.send(d)
				default:
					return
				}
			}
		}
	}
}

func (m *Manager) send(d delivery) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.hook.URL, bytes.NewReader(d.payload))
	if err != nil {
		log.Printf("[Webhook] 创建请求失败: %v", err)
		m.failed.Add(1)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GoClaw-Webhook/1.0")
	if d.sig != "" {
		req.Header.Set("X-GoClaw-Signature", d.sig)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		log.Printf("[Webhook] 发送失败 → %s: %v", d.hook.URL, err)
		m.failed.Add(1)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		m.sent.Add(1)
	} else {
		log.Printf("[Webhook] 非 2xx 响应 → %s: %d", d.hook.URL, resp.StatusCode)
		m.failed.Add(1)
	}
}

// Emit 发送事件到所有匹配的 webhook
func (m *Manager) Emit(event EventType, session string, data map[string]string) {
	payload := Payload{
		Event:     event,
		Timestamp: time.Now(),
		Session:   session,
		Data:      data,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, hook := range m.hooks {
		if !m.matchesEvent(hook, event) {
			continue
		}

		sig := ""
		if hook.Secret != "" {
			sig = sign(body, hook.Secret)
		}

		select {
		case m.queue <- delivery{hook: hook, payload: body, sig: sig}:
		default:
			log.Printf("[Webhook] 队列已满，丢弃事件 %s → %s", event, hook.URL)
			m.failed.Add(1)
		}
	}
}

func (m *Manager) matchesEvent(hook Hook, event EventType) bool {
	if len(hook.Events) == 0 {
		return true // 空列表 = 订阅全部
	}
	for _, e := range hook.Events {
		if e == event {
			return true
		}
	}
	return false
}

// Stats 返回发送统计
func (m *Manager) Stats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]interface{}{
		"hooks":  len(m.hooks),
		"sent":   m.sent.Load(),
		"failed": m.failed.Load(),
		"queue":  len(m.queue),
	}
}

// AddHook 动态添加 webhook
func (m *Manager) AddHook(hook Hook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hooks = append(m.hooks, hook)
}

// RemoveHook 按 URL 移除 webhook
func (m *Manager) RemoveHook(url string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, h := range m.hooks {
		if h.URL == url {
			m.hooks = append(m.hooks[:i], m.hooks[i+1:]...)
			return true
		}
	}
	return false
}

// ListHooks 列出所有 hook（脱敏 secret）
func (m *Manager) ListHooks() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]map[string]interface{}, len(m.hooks))
	for i, h := range m.hooks {
		result[i] = map[string]interface{}{
			"url":       h.URL,
			"has_secret": h.Secret != "",
			"events":    h.Events,
		}
	}
	return result
}

// Close 关闭 webhook 管理器
func (m *Manager) Close() {
	close(m.done)
}

// sign 使用 HMAC-SHA256 签名
func sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
