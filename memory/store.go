package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store 负责记忆文件的读写
type Store struct {
	baseDir string // memory_data 目录路径
}

// NewStore 创建 Store
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// SubStore 创建子目录 Store（用于群聊中按用户隔离 user.md / memory.md）
func (s *Store) SubStore(namespace string) *Store {
	return &Store{baseDir: filepath.Join(s.baseDir, namespace)}
}

// ReadSoul 读取 soul.md
func (s *Store) ReadSoul() (string, error) {
	return s.readFile("soul.md")
}

// ReadUser 读取 user.md
func (s *Store) ReadUser() (string, error) {
	return s.readFile("user.md")
}

// ReadMemory 读取 memory.md
func (s *Store) ReadMemory() (string, error) {
	return s.readFile("memory.md")
}

// WriteUser 写入 user.md
func (s *Store) WriteUser(content string) error {
	return s.writeFile("user.md", content)
}

// WriteMemory 写入 memory.md
func (s *Store) WriteMemory(content string) error {
	return s.writeFile("memory.md", content)
}

// LogEntry 对话日志条目
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"` // "user", "assistant", "tool"
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolArgs  string `json:"tool_args,omitempty"`
}

// AppendLog 追加一条对话日志
func (s *Store) AppendLog(entry LogEntry) error {
	today := time.Now().Format("2006-01-02")
	logDir := filepath.Join(s.baseDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}
	logFile := filepath.Join(logDir, today+".jsonl")

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// ReadTodayLogs 读取今天的对话日志
func (s *Store) ReadTodayLogs() ([]LogEntry, error) {
	today := time.Now().Format("2006-01-02")
	return s.ReadLogs(today)
}

// ReadLogs 读取指定日期的日志
func (s *Store) ReadLogs(date string) ([]LogEntry, error) {
	logFile := filepath.Join(s.baseDir, "logs", date+".jsonl")
	data, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []LogEntry
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var entry LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *Store) readFile(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.baseDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (s *Store) writeFile(name string, content string) error {
	return os.WriteFile(filepath.Join(s.baseDir, name), []byte(content), 0644)
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
