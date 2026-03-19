package tools

import (
	"context"
	"fmt"
	"sync"
)

// ToolFunc 是工具的执行函数签名
type ToolFunc func(ctx context.Context, args map[string]interface{}) (string, error)

// ToolDef 定义一个工具
type ToolDef struct {
	Name        string
	Description string
	Parameters  []ParamDef
	Fn          ToolFunc
}

// ParamDef 定义工具参数
type ParamDef struct {
	Name        string
	Type        string // "string", "number", "boolean"
	Description string
	Required    bool
}

// Registry 工具注册中心
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*ToolDef
}

// NewRegistry 创建工具注册中心
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*ToolDef),
	}
}

// Register 注册一个工具
func (r *Registry) Register(tool *ToolDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name] = tool
}

// Get 获取工具
func (r *Registry) Get(name string) (*ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Execute 执行工具
func (r *Registry) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	tool, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tool not found: %s", name)
	}
	return tool.Fn(ctx, args)
}

// Names 返回所有已注册工具的名称列表
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}
