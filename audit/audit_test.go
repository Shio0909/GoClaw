package audit

import (
	"testing"
	"time"
)

func TestNewLog(t *testing.T) {
	l := NewLog(100)
	if l.Count() != 0 {
		t.Fatal("new log should be empty")
	}
	if l.capacity != 100 {
		t.Fatalf("capacity: want 100, got %d", l.capacity)
	}
}

func TestNewLogDefaultCapacity(t *testing.T) {
	l := NewLog(0)
	if l.capacity != 1000 {
		t.Fatalf("default capacity: want 1000, got %d", l.capacity)
	}
}

func TestEmitAndQuery(t *testing.T) {
	l := NewLog(100)

	l.Emit(EventChatStart, "sess1", "user sent hello", "127.0.0.1", nil)
	l.Emit(EventToolCall, "sess1", "file_read", "127.0.0.1", map[string]string{"tool": "file_read"})
	l.Emit(EventChatEnd, "sess1", "completed", "127.0.0.1", nil)

	if l.Count() != 3 {
		t.Fatalf("count: want 3, got %d", l.Count())
	}

	// query all
	all := l.Query("", 0, 0)
	if len(all) != 3 {
		t.Fatalf("query all: want 3, got %d", len(all))
	}
	// should be in chronological order
	if all[0].Type != EventChatStart {
		t.Fatalf("first event: want chat_start, got %s", all[0].Type)
	}
	if all[2].Type != EventChatEnd {
		t.Fatalf("last event: want chat_end, got %s", all[2].Type)
	}
}

func TestQueryByType(t *testing.T) {
	l := NewLog(100)
	l.Emit(EventChatStart, "s1", "", "", nil)
	l.Emit(EventToolCall, "s1", "shell", "", nil)
	l.Emit(EventToolCall, "s1", "file_read", "", nil)
	l.Emit(EventChatEnd, "s1", "", "", nil)

	tools := l.Query(EventToolCall, 0, 0)
	if len(tools) != 2 {
		t.Fatalf("tool events: want 2, got %d", len(tools))
	}
}

func TestQueryLimit(t *testing.T) {
	l := NewLog(100)
	for i := 0; i < 10; i++ {
		l.Emit(EventChatStart, "s1", "", "", nil)
	}

	limited := l.Query("", 3, 0)
	if len(limited) != 3 {
		t.Fatalf("limited: want 3, got %d", len(limited))
	}
}

func TestQuerySinceID(t *testing.T) {
	l := NewLog(100)
	l.Emit(EventChatStart, "s1", "", "", nil) // id=1
	l.Emit(EventChatEnd, "s1", "", "", nil)   // id=2
	l.Emit(EventToolCall, "s1", "", "", nil)  // id=3

	since := l.Query("", 0, 1)
	if len(since) != 2 {
		t.Fatalf("since id=1: want 2, got %d", len(since))
	}
	if since[0].ID != 2 {
		t.Fatalf("first after since: want id=2, got id=%d", since[0].ID)
	}
}

func TestRingBufferOverflow(t *testing.T) {
	l := NewLog(3)
	l.Emit(EventChatStart, "s1", "first", "", nil)
	l.Emit(EventChatEnd, "s1", "second", "", nil)
	l.Emit(EventToolCall, "s1", "third", "", nil)
	l.Emit(EventError, "s1", "fourth", "", nil) // overflow: evicts first

	if l.Count() != 3 {
		t.Fatalf("count after overflow: want 3, got %d", l.Count())
	}

	all := l.Query("", 0, 0)
	if len(all) != 3 {
		t.Fatalf("query all after overflow: want 3, got %d", len(all))
	}
	if all[0].Detail != "second" {
		t.Fatalf("oldest after overflow: want 'second', got '%s'", all[0].Detail)
	}
	if all[2].Detail != "fourth" {
		t.Fatalf("newest after overflow: want 'fourth', got '%s'", all[2].Detail)
	}
}

func TestCounts(t *testing.T) {
	l := NewLog(100)
	l.Emit(EventChatStart, "", "", "", nil)
	l.Emit(EventChatStart, "", "", "", nil)
	l.Emit(EventToolCall, "", "", "", nil)
	l.Emit(EventError, "", "", "", nil)

	counts := l.Counts()
	if counts[EventChatStart] != 2 {
		t.Fatalf("chat_start count: want 2, got %d", counts[EventChatStart])
	}
	if counts[EventToolCall] != 1 {
		t.Fatalf("tool_call count: want 1, got %d", counts[EventToolCall])
	}
}

func TestEventTimestamp(t *testing.T) {
	l := NewLog(10)
	before := time.Now()
	l.Emit(EventChatStart, "s1", "test", "", nil)
	after := time.Now()

	events := l.Query("", 0, 0)
	if len(events) != 1 {
		t.Fatal("expected 1 event")
	}
	ts := events[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Fatalf("timestamp out of range: %v not in [%v, %v]", ts, before, after)
	}
}

func TestEventMeta(t *testing.T) {
	l := NewLog(10)
	l.Emit(EventToolCall, "s1", "shell", "10.0.0.1", map[string]string{
		"tool":    "shell",
		"command": "ls",
	})

	events := l.Query("", 0, 0)
	if events[0].Meta["tool"] != "shell" {
		t.Fatal("meta tool mismatch")
	}
	if events[0].IP != "10.0.0.1" {
		t.Fatal("IP mismatch")
	}
}

func TestAutoIncrementID(t *testing.T) {
	l := NewLog(10)
	l.Emit(EventChatStart, "", "", "", nil)
	l.Emit(EventChatEnd, "", "", "", nil)

	events := l.Query("", 0, 0)
	if events[0].ID != 1 || events[1].ID != 2 {
		t.Fatalf("IDs: want 1,2, got %d,%d", events[0].ID, events[1].ID)
	}
}
