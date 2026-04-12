package memory

// Provider 记忆存储后端接口
// 实现此接口可接入不同的存储方式（文件、Redis、SQLite、向量数据库等）
type Provider interface {
	// ReadSoul 读取人格描述
	ReadSoul() (string, error)

	// ReadUser 读取用户画像
	ReadUser() (string, error)

	// ReadMemory 读取长期记忆
	ReadMemory() (string, error)

	// WriteUser 写入用户画像
	WriteUser(content string) error

	// WriteMemory 写入长期记忆
	WriteMemory(content string) error

	// AppendLog 追加一条对话日志
	AppendLog(entry LogEntry) error

	// ReadTodayLogs 读取今天的对话日志
	ReadTodayLogs() ([]LogEntry, error)

	// ReadLogs 读取指定日期的日志
	ReadLogs(date string) ([]LogEntry, error)

	// SubProvider 创建子命名空间的 Provider（用于群聊按用户隔离）
	SubProvider(namespace string) Provider
}
