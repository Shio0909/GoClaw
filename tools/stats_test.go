package tools

import (
	"sync"
	"testing"
	"time"
)

func TestToolStatsRecordAndSnapshot(t *testing.T) {
	stats := &ToolStats{stats: make(map[string]*toolStat)}

	stats.RecordCall("read_file", 50*time.Millisecond, nil, 0)
	stats.RecordCall("read_file", 100*time.Millisecond, nil, 0)
	stats.RecordCall("write_file", 200*time.Millisecond, nil, 0)

	snap := stats.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(snap))
	}

	// snap[0] should be read_file (2 calls, sorted by count desc)
	if snap[0].Name != "read_file" {
		t.Errorf("expected read_file first, got %s", snap[0].Name)
	}
	if snap[0].Calls != 2 {
		t.Errorf("expected 2 calls, got %d", snap[0].Calls)
	}
	if snap[0].Errors != 0 {
		t.Errorf("expected 0 errors, got %d", snap[0].Errors)
	}
}

func TestToolStatsRecordErrors(t *testing.T) {
	stats := &ToolStats{stats: make(map[string]*toolStat)}

	stats.RecordCall("web_fetch", 300*time.Millisecond, nil, 0)
	stats.RecordCall("web_fetch", 500*time.Millisecond, errForTest("timeout"), 2)

	snap := stats.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(snap))
	}
	if snap[0].Calls != 2 {
		t.Errorf("expected 2 calls, got %d", snap[0].Calls)
	}
	if snap[0].Errors != 1 {
		t.Errorf("expected 1 error, got %d", snap[0].Errors)
	}
	if snap[0].Retries != 2 {
		t.Errorf("expected 2 retries, got %d", snap[0].Retries)
	}
}

func TestToolStatsSummary(t *testing.T) {
	stats := &ToolStats{stats: make(map[string]*toolStat)}

	stats.RecordCall("read_file", 10*time.Millisecond, nil, 0)
	stats.RecordCall("web_fetch", 200*time.Millisecond, errForTest("fail"), 1)

	summary := stats.Summary()
	if summary["total_calls"].(int64) != 2 {
		t.Errorf("expected total_calls=2, got %v", summary["total_calls"])
	}
	if summary["total_errors"].(int64) != 1 {
		t.Errorf("expected total_errors=1, got %v", summary["total_errors"])
	}
	if summary["unique_tools"].(int) != 2 {
		t.Errorf("expected unique_tools=2, got %v", summary["unique_tools"])
	}
}

func TestToolStatsConcurrency(t *testing.T) {
	stats := &ToolStats{stats: make(map[string]*toolStat)}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "tool_a"
			if i%2 == 0 {
				name = "tool_b"
			}
			stats.RecordCall(name, time.Millisecond, nil, 0)
		}(i)
	}
	wg.Wait()

	snap := stats.Snapshot()
	var total int64
	for _, e := range snap {
		total += e.Calls
	}
	if total != 100 {
		t.Errorf("expected 100 total calls, got %d", total)
	}
}

func TestToolStatsAvgMs(t *testing.T) {
	stats := &ToolStats{stats: make(map[string]*toolStat)}
	stats.RecordCall("slow_tool", 100*time.Millisecond, nil, 0)
	stats.RecordCall("slow_tool", 200*time.Millisecond, nil, 0)

	snap := stats.Snapshot()
	if snap[0].AvgMs < 100 || snap[0].AvgMs > 200 {
		t.Errorf("expected avg_ms between 100-200, got %f", snap[0].AvgMs)
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }
