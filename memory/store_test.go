package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadWrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// 初始读取应返回空字符串
	soul, err := store.ReadSoul()
	if err != nil {
		t.Fatalf("ReadSoul error: %v", err)
	}
	if soul != "" {
		t.Fatalf("expected empty soul, got %s", soul)
	}

	// 写入并读取 user.md
	if err := store.WriteUser("test user"); err != nil {
		t.Fatalf("WriteUser error: %v", err)
	}
	user, err := store.ReadUser()
	if err != nil {
		t.Fatalf("ReadUser error: %v", err)
	}
	if user != "test user" {
		t.Fatalf("expected 'test user', got %s", user)
	}

	// 写入并读取 memory.md
	if err := store.WriteMemory("test memory"); err != nil {
		t.Fatalf("WriteMemory error: %v", err)
	}
	mem, err := store.ReadMemory()
	if err != nil {
		t.Fatalf("ReadMemory error: %v", err)
	}
	if mem != "test memory" {
		t.Fatalf("expected 'test memory', got %s", mem)
	}
}

func TestStoreAppendLog(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	err := store.AppendLog(LogEntry{
		Timestamp: "2024-01-01T00:00:00Z",
		Role:      "user",
		Content:   "hello",
	})
	if err != nil {
		t.Fatalf("AppendLog error: %v", err)
	}

	// 验证日志目录被创建
	logDir := filepath.Join(dir, "logs")
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		t.Fatal("expected logs directory to be created")
	}

	logs, err := store.ReadTodayLogs()
	if err != nil {
		t.Fatalf("ReadTodayLogs error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].Content != "hello" {
		t.Fatalf("expected content 'hello', got %s", logs[0].Content)
	}
}
