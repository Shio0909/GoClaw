package tools

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ToolStats 工具调用统计
type ToolStats struct {
	mu    sync.RWMutex
	stats map[string]*toolStat
}

type toolStat struct {
	calls    atomic.Int64
	errors   atomic.Int64
	retries  atomic.Int64
	totalMs  atomic.Int64 // 累计耗时毫秒
	lastUsed atomic.Int64 // Unix 时间戳
}

// ToolStatEntry 单个工具的统计快照
type ToolStatEntry struct {
	Name      string  `json:"name"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	Retries   int64   `json:"retries"`
	AvgMs     float64 `json:"avg_ms"`
	LastUsed  string  `json:"last_used,omitempty"`
}

var globalStats = &ToolStats{stats: make(map[string]*toolStat)}

// GetGlobalToolStats 获取全局工具统计
func GetGlobalToolStats() *ToolStats {
	return globalStats
}

func (s *ToolStats) getStat(name string) *toolStat {
	s.mu.RLock()
	st, ok := s.stats[name]
	s.mu.RUnlock()
	if ok {
		return st
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok = s.stats[name]; ok {
		return st
	}
	st = &toolStat{}
	s.stats[name] = st
	return st
}

// RecordCall 记录一次工具调用
func (s *ToolStats) RecordCall(name string, duration time.Duration, err error, retries int) {
	st := s.getStat(name)
	st.calls.Add(1)
	if err != nil {
		st.errors.Add(1)
	}
	if retries > 0 {
		st.retries.Add(int64(retries))
	}
	st.totalMs.Add(duration.Milliseconds())
	st.lastUsed.Store(time.Now().Unix())
}

// Snapshot 获取所有工具的统计快照
func (s *ToolStats) Snapshot() []ToolStatEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]ToolStatEntry, 0, len(s.stats))
	for name, st := range s.stats {
		calls := st.calls.Load()
		entry := ToolStatEntry{
			Name:    name,
			Calls:   calls,
			Errors:  st.errors.Load(),
			Retries: st.retries.Load(),
		}
		if calls > 0 {
			entry.AvgMs = float64(st.totalMs.Load()) / float64(calls)
		}
		if ts := st.lastUsed.Load(); ts > 0 {
			entry.LastUsed = time.Unix(ts, 0).Format(time.RFC3339)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Calls > entries[j].Calls
	})
	return entries
}

// Summary 汇总统计
func (s *ToolStats) Summary() map[string]interface{} {
	entries := s.Snapshot()
	var totalCalls, totalErrors, totalRetries int64
	for _, e := range entries {
		totalCalls += e.Calls
		totalErrors += e.Errors
		totalRetries += e.Retries
	}
	return map[string]interface{}{
		"total_calls":   totalCalls,
		"total_errors":  totalErrors,
		"total_retries": totalRetries,
		"unique_tools":  len(entries),
		"tools":         entries,
	}
}
