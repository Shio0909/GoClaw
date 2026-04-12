package agent

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// PoolStrategy Key 选择策略
type PoolStrategy string

const (
	StrategyRoundRobin PoolStrategy = "round_robin" // 轮询
	StrategyFillFirst  PoolStrategy = "fill_first"  // 优先用一个，用完换下一个
	StrategyLeastUsed  PoolStrategy = "least_used"  // 最少使用
)

// PoolKey 池中的一个 Key
type PoolKey struct {
	APIKey    string      `json:"api_key"`
	Provider  string      `json:"provider,omitempty"`
	BaseURL   string      `json:"base_url,omitempty"`
	Disabled  bool        `json:"disabled"`
	LastError ErrorReason `json:"last_error,omitempty"`
	ErrorAt   time.Time   `json:"error_at,omitempty"`
	UseCount  int64       `json:"use_count"`
}

// isAvailable 判断 key 是否可用
func (k *PoolKey) isAvailable() bool {
	if k.Disabled {
		return false
	}
	// 认证错误的 key 不再使用
	if k.LastError == ReasonAuth || k.LastError == ReasonBilling {
		return false
	}
	// 速率限制的 key 冷却 60 秒后可复用
	if k.LastError == ReasonRateLimit && time.Since(k.ErrorAt) < 60*time.Second {
		return false
	}
	return true
}

// CredentialPool 凭证池管理多个 API Key
type CredentialPool struct {
	mu       sync.Mutex
	keys     []PoolKey
	strategy PoolStrategy
	cursor   int    // round_robin 用
	savePath string // 持久化路径（可选）
}

// NewCredentialPool 创建凭证池
func NewCredentialPool(strategy PoolStrategy) *CredentialPool {
	return &CredentialPool{
		strategy: strategy,
	}
}

// AddKey 添加一个 Key
func (p *CredentialPool) AddKey(apiKey, provider, baseURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// 去重
	for _, k := range p.keys {
		if k.APIKey == apiKey {
			return
		}
	}
	p.keys = append(p.keys, PoolKey{
		APIKey:   apiKey,
		Provider: provider,
		BaseURL:  baseURL,
	})
}

// GetKey 根据策略获取一个可用的 Key
func (p *CredentialPool) GetKey() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	available := p.availableIndices()
	if len(available) == 0 {
		return "", false
	}

	var idx int
	switch p.strategy {
	case StrategyRoundRobin:
		idx = p.nextRoundRobin(available)
	case StrategyLeastUsed:
		idx = p.leastUsed(available)
	default: // fill_first
		idx = available[0]
	}

	p.keys[idx].UseCount++
	return p.keys[idx].APIKey, true
}

// NextKey 获取当前 key 之后的下一个可用 key（用于轮换）
func (p *CredentialPool) NextKey(currentKey string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	available := p.availableIndices()
	if len(available) == 0 {
		return "", false
	}

	// 找到当前 key 的位置
	currentIdx := -1
	for _, i := range available {
		if p.keys[i].APIKey == currentKey {
			currentIdx = i
			break
		}
	}

	// 取可用列表中当前 key 之后的下一个
	for _, i := range available {
		if i != currentIdx {
			p.keys[i].UseCount++
			return p.keys[i].APIKey, true
		}
	}
	return "", false
}

// RecordSuccess 记录 key 使用成功
func (p *CredentialPool) RecordSuccess(apiKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.keys {
		if p.keys[i].APIKey == apiKey {
			// 清除之前的错误状态（速率限制恢复后重新可用）
			if p.keys[i].LastError == ReasonRateLimit {
				p.keys[i].LastError = ""
			}
			break
		}
	}
	p.save()
}

// RecordFailure 记录 key 使用失败
func (p *CredentialPool) RecordFailure(apiKey string, reason ErrorReason) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.keys {
		if p.keys[i].APIKey == apiKey {
			p.keys[i].LastError = reason
			p.keys[i].ErrorAt = time.Now()
			log.Printf("[Pool] Key ...%s 标记为 %s", maskKey(apiKey), reason)
			break
		}
	}
	p.save()
}

// SetSavePath 设置持久化路径
func (p *CredentialPool) SetSavePath(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.savePath = path
}

// Size 返回池中 key 总数
func (p *CredentialPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

// AvailableCount 返回当前可用的 key 数量
func (p *CredentialPool) AvailableCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.availableIndices())
}

// LoadFromFile 从 JSON 文件加载池状态
func (p *CredentialPool) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.savePath = path
	return json.Unmarshal(data, &p.keys)
}

// --- 内部方法（必须持有锁） ---

func (p *CredentialPool) availableIndices() []int {
	var result []int
	for i := range p.keys {
		if p.keys[i].isAvailable() {
			result = append(result, i)
		}
	}
	return result
}

func (p *CredentialPool) nextRoundRobin(available []int) int {
	for _, i := range available {
		if i >= p.cursor {
			p.cursor = i + 1
			return i
		}
	}
	// 回绕
	p.cursor = available[0] + 1
	return available[0]
}

func (p *CredentialPool) leastUsed(available []int) int {
	minIdx := available[0]
	minCount := p.keys[minIdx].UseCount
	for _, i := range available[1:] {
		if p.keys[i].UseCount < minCount {
			minIdx = i
			minCount = p.keys[i].UseCount
		}
	}
	return minIdx
}

func (p *CredentialPool) save() {
	if p.savePath == "" {
		return
	}
	data, err := json.MarshalIndent(p.keys, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(p.savePath, data, 0644)
}
