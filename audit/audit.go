package audit

import (
	"encoding/json"
	"sync"
	"time"
)

// EventType 审计事件类型
type EventType string

const (
	EventChatStart     EventType = "chat_start"
	EventChatEnd       EventType = "chat_end"
	EventToolCall      EventType = "tool_call"
	EventAuthFailure   EventType = "auth_failure"
	EventConfigReload  EventType = "config_reload"
	EventSessionCreate EventType = "session_create"
	EventSessionDelete EventType = "session_delete"
	EventSessionFork   EventType = "session_fork"
	EventRateLimit     EventType = "rate_limit"
	EventError         EventType = "error"
	EventShutdown      EventType = "shutdown"
)

// Event 审计事件
type Event struct {
	ID        int64             `json:"id"`
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Session   string            `json:"session,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	IP        string            `json:"ip,omitempty"`
}

// Log 审计日志（环形缓冲区，无锁读写分离）
type Log struct {
	mu       sync.RWMutex
	events   []Event
	capacity int
	head     int // 下一个写入位置
	count    int
	nextID   int64
}

// NewLog 创建审计日志，capacity 为最大保留事件数
func NewLog(capacity int) *Log {
	if capacity <= 0 {
		capacity = 1000
	}
	return &Log{
		events:   make([]Event, capacity),
		capacity: capacity,
	}
}

// Emit 记录一个审计事件
func (l *Log) Emit(typ EventType, session, detail, ip string, meta map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.nextID++
	e := Event{
		ID:        l.nextID,
		Type:      typ,
		Timestamp: time.Now(),
		Session:   session,
		Detail:    detail,
		IP:        ip,
		Meta:      meta,
	}

	l.events[l.head] = e
	l.head = (l.head + 1) % l.capacity
	if l.count < l.capacity {
		l.count++
	}
}

// Query 查询审计事件
// typ 为空则不筛选类型，limit <= 0 则返回全部，sinceID > 0 只返回 ID 大于此值的事件
func (l *Log) Query(typ EventType, limit int, sinceID int64) []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if l.count == 0 {
		return []Event{}
	}

	// 从最新到最旧遍历
	var results []Event
	for i := 0; i < l.count; i++ {
		idx := (l.head - 1 - i + l.capacity) % l.capacity
		e := l.events[idx]
		if sinceID > 0 && e.ID <= sinceID {
			break // 环形缓冲区中 ID 递增，可以提前终止
		}
		if typ != "" && e.Type != typ {
			continue
		}
		results = append(results, e)
		if limit > 0 && len(results) >= limit {
			break
		}
	}

	// 反转为时间升序
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return results
}

// Count 返回当前事件总数
func (l *Log) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.count
}

// Counts 按类型统计事件数量
func (l *Log) Counts() map[EventType]int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	counts := make(map[EventType]int)
	for i := 0; i < l.count; i++ {
		idx := (l.head - 1 - i + l.capacity) % l.capacity
		counts[l.events[idx].Type]++
	}
	return counts
}

// MarshalJSON 用于序列化日志摘要
func (l *Log) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"total":  l.Count(),
		"counts": l.Counts(),
	})
}
