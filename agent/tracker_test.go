package agent

import (
	"fmt"
	"testing"
	"time"
)

func TestTurnTracker_StartEnd(t *testing.T) {
	tracker := NewTurnTracker(10)

	tracker.StartTurn("hello", "gpt-4")
	time.Sleep(5 * time.Millisecond)
	tracker.EndTurn("world", false, nil)

	turns := tracker.GetTurns()
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].UserInput != "hello" {
		t.Errorf("expected input 'hello', got %q", turns[0].UserInput)
	}
	if turns[0].Response != "world" {
		t.Errorf("expected response 'world', got %q", turns[0].Response)
	}
	if turns[0].ModelUsed != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", turns[0].ModelUsed)
	}
	if turns[0].Duration < 5*time.Millisecond {
		t.Errorf("expected duration >= 5ms, got %v", turns[0].Duration)
	}
}

func TestTurnTracker_ToolCalls(t *testing.T) {
	tracker := NewTurnTracker(10)

	tracker.StartTurn("search something", "gpt-4")
	tracker.RecordToolCall("web_fetch", map[string]interface{}{"url": "https://example.com"}, "page content", nil, 100*time.Millisecond)
	tracker.RecordToolCall("file_read", map[string]interface{}{"path": "/tmp/test"}, "", fmt.Errorf("not found"), 5*time.Millisecond)
	tracker.EndTurn("here is the result", false, nil)

	turns := tracker.GetTurns()
	if len(turns[0].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(turns[0].ToolCalls))
	}
	if turns[0].ToolCalls[0].ToolName != "web_fetch" {
		t.Errorf("expected 'web_fetch', got %q", turns[0].ToolCalls[0].ToolName)
	}
	if !turns[0].ToolCalls[0].Success {
		t.Error("expected web_fetch to be successful")
	}
	if turns[0].ToolCalls[1].Success {
		t.Error("expected file_read to be failed")
	}
	if turns[0].ToolCalls[1].Error != "not found" {
		t.Errorf("expected error 'not found', got %q", turns[0].ToolCalls[1].Error)
	}
}

func TestTurnTracker_MaxKeep(t *testing.T) {
	tracker := NewTurnTracker(3)

	for i := 0; i < 5; i++ {
		tracker.StartTurn(fmt.Sprintf("input-%d", i), "model")
		tracker.EndTurn(fmt.Sprintf("output-%d", i), false, nil)
	}

	turns := tracker.GetTurns()
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns (maxKeep), got %d", len(turns))
	}
	if turns[0].UserInput != "input-2" {
		t.Errorf("expected oldest kept turn to be 'input-2', got %q", turns[0].UserInput)
	}
}

func TestTurnTracker_RecentTurns(t *testing.T) {
	tracker := NewTurnTracker(10)

	for i := 0; i < 5; i++ {
		tracker.StartTurn(fmt.Sprintf("input-%d", i), "model")
		tracker.EndTurn(fmt.Sprintf("output-%d", i), false, nil)
	}

	recent := tracker.GetRecentTurns(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent turns, got %d", len(recent))
	}
	if recent[0].UserInput != "input-3" {
		t.Errorf("expected 'input-3', got %q", recent[0].UserInput)
	}
}

func TestTurnTracker_Summary(t *testing.T) {
	tracker := NewTurnTracker(10)

	tracker.StartTurn("q1", "model")
	tracker.RecordToolCall("shell", nil, "ok", nil, 50*time.Millisecond)
	tracker.RecordToolCall("shell", nil, "ok", nil, 30*time.Millisecond)
	tracker.RecordToolCall("file_read", nil, "data", nil, 10*time.Millisecond)
	tracker.EndTurn("a1", false, nil)

	tracker.StartTurn("q2", "model")
	tracker.RecordToolCall("web_fetch", nil, "", fmt.Errorf("timeout"), 5000*time.Millisecond)
	tracker.EndTurn("a2", false, nil)

	summary := tracker.Summary()
	if summary["total_turns"].(int) != 2 {
		t.Errorf("expected 2 total turns, got %v", summary["total_turns"])
	}
	if summary["total_tool_calls"].(int) != 4 {
		t.Errorf("expected 4 total tool calls, got %v", summary["total_tool_calls"])
	}
	if summary["total_tool_errors"].(int) != 1 {
		t.Errorf("expected 1 tool error, got %v", summary["total_tool_errors"])
	}
	if summary["top_tool"].(string) != "shell" {
		t.Errorf("expected top tool 'shell', got %v", summary["top_tool"])
	}
}

func TestTurnTracker_Fallback(t *testing.T) {
	tracker := NewTurnTracker(10)

	tracker.StartTurn("hard question", "gpt-4")
	tracker.EndTurn("fallback response", true, nil)

	turns := tracker.GetTurns()
	if !turns[0].WasFallback {
		t.Error("expected WasFallback to be true")
	}
}

func TestTurnTracker_Error(t *testing.T) {
	tracker := NewTurnTracker(10)

	tracker.StartTurn("bad question", "model")
	tracker.EndTurn("", false, fmt.Errorf("context deadline exceeded"))

	turns := tracker.GetTurns()
	if turns[0].ErrorMessage != "context deadline exceeded" {
		t.Errorf("expected error message, got %q", turns[0].ErrorMessage)
	}
}

func TestTurnTracker_LongResultTruncation(t *testing.T) {
	tracker := NewTurnTracker(10)
	longResult := make([]byte, 1000)
	for i := range longResult {
		longResult[i] = 'x'
	}

	tracker.StartTurn("q", "model")
	tracker.RecordToolCall("test", nil, string(longResult), nil, time.Millisecond)
	tracker.EndTurn("a", false, nil)

	turns := tracker.GetTurns()
	if len(turns[0].ToolCalls[0].Result) > 510 {
		t.Errorf("expected truncated result, got length %d", len(turns[0].ToolCalls[0].Result))
	}
}

func TestTurnTracker_NilCurrentGuard(t *testing.T) {
	tracker := NewTurnTracker(10)
	// Should not panic when no turn is started
	tracker.RecordToolCall("test", nil, "ok", nil, time.Millisecond)
	tracker.EndTurn("test", false, nil)

	if len(tracker.GetTurns()) != 0 {
		t.Error("expected 0 turns when no turn was started")
	}
}
