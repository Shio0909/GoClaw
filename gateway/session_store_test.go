package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestSessionStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	snap := &SessionSnapshot{
		ID: "test-session-1",
		History: []*schema.Message{
			{Role: schema.User, Content: "hello"},
			{Role: schema.Assistant, Content: "hi there"},
		},
		SavedAt:  time.Now(),
		LastUsed: time.Now(),
	}

	if err := store.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load("test-session-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != snap.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, snap.ID)
	}
	if len(loaded.History) != 2 {
		t.Fatalf("History len: got %d, want 2", len(loaded.History))
	}
	if loaded.History[0].Content != "hello" {
		t.Errorf("History[0].Content: got %q, want %q", loaded.History[0].Content, "hello")
	}
	if loaded.History[1].Role != schema.Assistant {
		t.Errorf("History[1].Role: got %v, want %v", loaded.History[1].Role, schema.Assistant)
	}
}

func TestSessionStoreLoadAll(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	for _, id := range []string{"s1", "s2", "s3"} {
		snap := &SessionSnapshot{
			ID:       id,
			History:  []*schema.Message{{Role: schema.User, Content: "msg from " + id}},
			SavedAt:  time.Now(),
			LastUsed: time.Now(),
		}
		if err := store.Save(snap); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}

	all, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("LoadAll count: got %d, want 3", len(all))
	}
}

func TestSessionStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	snap := &SessionSnapshot{
		ID:       "to-delete",
		History:  []*schema.Message{{Role: schema.User, Content: "bye"}},
		SavedAt:  time.Now(),
		LastUsed: time.Now(),
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Load("to-delete")
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist error after delete, got %v", err)
	}
}

func TestSessionStoreDeleteNonExistent(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	// 删除不存在的 ID 不应报错
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete nonexistent should not error, got %v", err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"path/to/file", "path_to_file"},
		{"a:b*c?d", "a_b_c_d"},
		{"normal-id-123", "normal-id-123"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeFilenameLongInput(t *testing.T) {
	long := ""
	for i := 0; i < 250; i++ {
		long += "a"
	}
	got := sanitizeFilename(long)
	if len(got) > 200 {
		t.Errorf("sanitizeFilename should truncate to 200 chars, got %d", len(got))
	}
}

func TestSessionStoreLoadAllEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	all, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected empty, got %d", len(all))
	}
}

func TestSessionStoreSkipsNonJSON(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	// 写入一个非 JSON 文件
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not json"), 0644)

	// 写入一个合法的 session
	snap := &SessionSnapshot{
		ID:       "valid",
		History:  []*schema.Message{{Role: schema.User, Content: "ok"}},
		SavedAt:  time.Now(),
		LastUsed: time.Now(),
	}
	store.Save(snap)

	all, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 (skip non-JSON), got %d", len(all))
	}
}
