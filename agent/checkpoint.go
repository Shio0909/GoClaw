package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileCheckPointStore 基于文件系统的 CheckPointStore 实现，
// 满足 eino adk.CheckPointStore (core.CheckPointStore) 接口:
//   - Get(ctx, key) ([]byte, bool, error)
//   - Set(ctx, key, value []byte) error
type FileCheckPointStore struct {
	dir string
	mu  sync.RWMutex
}

// NewFileCheckPointStore 创建文件系统 CheckPointStore。
// dir 为检查点存储目录，不存在则自动创建。
func NewFileCheckPointStore(dir string) (*FileCheckPointStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir: %w", err)
	}
	return &FileCheckPointStore{dir: dir}, nil
}

func (s *FileCheckPointStore) checkpointPath(key string) string {
	return filepath.Join(s.dir, key+".ckpt")
}

func (s *FileCheckPointStore) metaPath(key string) string {
	return filepath.Join(s.dir, key+".meta.json")
}

// Set 保存检查点数据到文件
func (s *FileCheckPointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.checkpointPath(key)
	if err := os.WriteFile(path, value, 0o644); err != nil {
		return fmt.Errorf("write checkpoint %s: %w", key, err)
	}

	meta := checkpointMeta{
		Key:       key,
		CreatedAt: time.Now().UTC(),
		Size:      len(value),
	}
	metaBytes, _ := json.Marshal(meta)
	_ = os.WriteFile(s.metaPath(key), metaBytes, 0o644)

	return nil
}

// Get 从文件读取检查点数据
func (s *FileCheckPointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.checkpointPath(key)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read checkpoint %s: %w", key, err)
	}
	return data, true, nil
}

// Delete 删除检查点
func (s *FileCheckPointStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = os.Remove(s.checkpointPath(key))
	_ = os.Remove(s.metaPath(key))
	return nil
}

// List 列出所有检查点元信息
func (s *FileCheckPointStore) List() ([]checkpointMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list checkpoint dir: %w", err)
	}

	var metas []checkpointMeta
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		var m checkpointMeta
		if json.Unmarshal(data, &m) == nil {
			metas = append(metas, m)
		}
	}

	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt.After(metas[j].CreatedAt)
	})
	return metas, nil
}

// CheckpointMetaInfo 检查点元信息（导出供 HTTP 层使用）
type CheckpointMetaInfo = checkpointMeta

// checkpointMeta 检查点元信息
type checkpointMeta struct {
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	Size      int       `json:"size"`
}

// MemoryCheckPointStore 内存 CheckPointStore（用于测试）
type MemoryCheckPointStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemoryCheckPointStore() *MemoryCheckPointStore {
	return &MemoryCheckPointStore{data: make(map[string][]byte)}
}

func (s *MemoryCheckPointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	s.data[key] = cp
	return nil
}

func (s *MemoryCheckPointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return nil, false, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, true, nil
}

func (s *MemoryCheckPointStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}
