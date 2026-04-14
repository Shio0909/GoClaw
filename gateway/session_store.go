package gateway

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// SessionSnapshot 会话快照，用于持久化到磁盘
type SessionSnapshot struct {
	ID       string            `json:"id"`
	History  []*schema.Message `json:"history"`
	SavedAt  time.Time         `json:"saved_at"`
	LastUsed time.Time         `json:"last_used"`
}

// SessionStore 会话持久化存储
type SessionStore struct {
	dir string
}

// NewSessionStore 创建会话存储，dir 为存储目录
func NewSessionStore(dir string) (*SessionStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &SessionStore{dir: dir}, nil
}

// Save 保存会话到磁盘
func (s *SessionStore) Save(snap *SessionSnapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	filename := sanitizeFilename(snap.ID) + ".json"
	return os.WriteFile(filepath.Join(s.dir, filename), data, 0644)
}

// Load 从磁盘加载单个会话
func (s *SessionStore) Load(id string) (*SessionSnapshot, error) {
	filename := sanitizeFilename(id) + ".json"
	data, err := os.ReadFile(filepath.Join(s.dir, filename))
	if err != nil {
		return nil, err
	}
	var snap SessionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// LoadAll 加载所有已保存的会话
func (s *SessionStore) LoadAll() ([]*SessionSnapshot, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var snapshots []*SessionSnapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			log.Printf("[SessionStore] 读取会话文件失败 %s: %v", entry.Name(), err)
			continue
		}
		var snap SessionSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			log.Printf("[SessionStore] 解析会话文件失败 %s: %v", entry.Name(), err)
			continue
		}
		snapshots = append(snapshots, &snap)
	}
	return snapshots, nil
}

// Delete 删除会话文件
func (s *SessionStore) Delete(id string) error {
	filename := sanitizeFilename(id) + ".json"
	err := os.Remove(filepath.Join(s.dir, filename))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// sanitizeFilename 清理 session ID 使其可安全用作文件名
func sanitizeFilename(id string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	name := replacer.Replace(id)
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}
