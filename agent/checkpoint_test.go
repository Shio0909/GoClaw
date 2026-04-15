package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCheckPointStore_SetGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	ctx := context.Background()
	key := "test-cp-1"
	data := []byte("checkpoint data 1234")

	if err := store.Set(ctx, key, data); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: expected ok=true")
	}
	if string(got) != string(data) {
		t.Fatalf("Get: got %q, want %q", got, data)
	}
}

func TestFileCheckPointStore_GetNotExist(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	_, ok, err := store.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("Get: expected ok=false for nonexistent key")
	}
}

func TestFileCheckPointStore_Delete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	ctx := context.Background()
	key := "del-test"
	_ = store.Set(ctx, key, []byte("data"))

	if err := store.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, _ := store.Get(ctx, key)
	if ok {
		t.Fatal("Get after Delete: expected ok=false")
	}
}

func TestFileCheckPointStore_List(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	ctx := context.Background()
	_ = store.Set(ctx, "cp-a", []byte("aaa"))
	_ = store.Set(ctx, "cp-b", []byte("bbbbbb"))

	metas, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("List: got %d, want 2", len(metas))
	}

	keys := map[string]bool{}
	for _, m := range metas {
		keys[m.Key] = true
	}
	if !keys["cp-a"] || !keys["cp-b"] {
		t.Fatalf("List: missing keys, got %v", keys)
	}
}

func TestFileCheckPointStore_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	ctx := context.Background()
	key := "overwrite"
	_ = store.Set(ctx, key, []byte("v1"))
	_ = store.Set(ctx, key, []byte("v2"))

	got, ok, _ := store.Get(ctx, key)
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != "v2" {
		t.Fatalf("got %q, want v2", got)
	}
}

func TestFileCheckPointStore_FileCreated(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("NewFileCheckPointStore: %v", err)
	}

	ctx := context.Background()
	_ = store.Set(ctx, "file-check", []byte("hello"))

	ckptPath := filepath.Join(dir, "file-check.ckpt")
	if _, err := os.Stat(ckptPath); os.IsNotExist(err) {
		t.Fatalf("checkpoint file not created: %s", ckptPath)
	}

	metaPath := filepath.Join(dir, "file-check.meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Fatalf("meta file not created: %s", metaPath)
	}
}

func TestMemoryCheckPointStore_SetGet(t *testing.T) {
	store := NewMemoryCheckPointStore()
	ctx := context.Background()

	_ = store.Set(ctx, "k1", []byte("data1"))
	got, ok, err := store.Get(ctx, "k1")
	if err != nil || !ok {
		t.Fatalf("Get: err=%v, ok=%v", err, ok)
	}
	if string(got) != "data1" {
		t.Fatalf("got %q, want data1", got)
	}
}

func TestMemoryCheckPointStore_Delete(t *testing.T) {
	store := NewMemoryCheckPointStore()
	ctx := context.Background()

	_ = store.Set(ctx, "k1", []byte("data"))
	_ = store.Delete("k1")

	_, ok, _ := store.Get(ctx, "k1")
	if ok {
		t.Fatal("expected ok=false after delete")
	}
}

func TestMemoryCheckPointStore_Isolation(t *testing.T) {
	store := NewMemoryCheckPointStore()
	ctx := context.Background()

	orig := []byte("original")
	_ = store.Set(ctx, "iso", orig)

	got, _, _ := store.Get(ctx, "iso")
	got[0] = 'X' // 修改返回值不应影响存储

	got2, _, _ := store.Get(ctx, "iso")
	if string(got2) != "original" {
		t.Fatalf("mutation leaked: got %q", got2)
	}
}
