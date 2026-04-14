package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// PluginType 插件执行方式
type PluginType string

const (
	PluginTypeScript PluginType = "script" // 外部脚本
	PluginTypeHTTP   PluginType = "http"   // HTTP API 调用
)

// PluginDef 插件定义（从 YAML/JSON 加载）
type PluginDef struct {
	Name        string     `yaml:"name" json:"name"`
	Description string     `yaml:"description" json:"description"`
	Type        PluginType `yaml:"type" json:"type"` // "script" or "http"
	Command     string     `yaml:"command" json:"command"`
	Timeout     int        `yaml:"timeout" json:"timeout"` // 秒
	Parameters  []ParamDef `yaml:"parameters" json:"parameters"`
	Retryable   bool       `yaml:"retryable" json:"retryable"`
}

// PluginManager 插件管理器
type PluginManager struct {
	mu      sync.RWMutex
	plugins map[string]*PluginDef
	dir     string // 插件目录
}

// NewPluginManager 创建插件管理器
func NewPluginManager(pluginDir string) *PluginManager {
	return &PluginManager{
		plugins: make(map[string]*PluginDef),
		dir:     pluginDir,
	}
}

// LoadDir 从目录加载所有插件定义
func (pm *PluginManager) LoadDir() (int, error) {
	if pm.dir == "" {
		return 0, nil
	}

	entries, err := os.ReadDir(pm.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read plugin dir: %w", err)
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}

		path := filepath.Join(pm.dir, name)
		if err := pm.LoadFile(path); err != nil {
			log.Printf("[Plugin] 加载失败 %s: %v", name, err)
			continue
		}
		loaded++
	}
	return loaded, nil
}

// LoadFile 从文件加载单个插件
func (pm *PluginManager) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read plugin: %w", err)
	}

	var def PluginDef
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".json" {
		if err := json.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parse json plugin: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parse yaml plugin: %w", err)
		}
	}

	if def.Name == "" {
		return fmt.Errorf("plugin name is required")
	}
	if def.Command == "" {
		return fmt.Errorf("plugin command is required")
	}
	if def.Type == "" {
		def.Type = PluginTypeScript
	}

	pm.mu.Lock()
	pm.plugins[def.Name] = &def
	pm.mu.Unlock()

	log.Printf("[Plugin] 已加载: %s (%s)", def.Name, def.Type)
	return nil
}

// RegisterAll 将所有插件注册为工具
func (pm *PluginManager) RegisterAll(registry *Registry) int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	count := 0
	for _, def := range pm.plugins {
		tool := pm.buildTool(def)
		registry.Register(tool)
		count++
	}
	return count
}

// Unload 卸载插件
func (pm *PluginManager) Unload(name string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if _, ok := pm.plugins[name]; ok {
		delete(pm.plugins, name)
		return true
	}
	return false
}

// List 列出所有插件
func (pm *PluginManager) List() []PluginDef {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	list := make([]PluginDef, 0, len(pm.plugins))
	for _, def := range pm.plugins {
		list = append(list, *def)
	}
	return list
}

// Get 获取插件定义
func (pm *PluginManager) Get(name string) (*PluginDef, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	def, ok := pm.plugins[name]
	return def, ok
}

func (pm *PluginManager) buildTool(def *PluginDef) *ToolDef {
	timeout := time.Duration(def.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	tool := &ToolDef{
		Name:        def.Name,
		Description: def.Description,
		Parameters:  def.Parameters,
		Retryable:   def.Retryable,
		Timeout:     timeout,
	}

	switch def.Type {
	case PluginTypeHTTP:
		tool.Fn = pm.makeHTTPFn(def)
	default:
		tool.Fn = pm.makeScriptFn(def)
	}
	return tool
}

func (pm *PluginManager) makeScriptFn(def *PluginDef) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", def.Command)

		env := os.Environ()
		for k, v := range args {
			env = append(env, fmt.Sprintf("PLUGIN_%s=%v", strings.ToUpper(k), v))
		}
		cmd.Env = env

		argsJSON, _ := json.Marshal(args)
		cmd.Stdin = bytes.NewReader(argsJSON)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("plugin %s: %v\nstderr: %s", def.Name, err, stderr.String())
		}
		return stdout.String(), nil
	}
}

func (pm *PluginManager) makeHTTPFn(def *PluginDef) ToolFunc {
	return func(ctx context.Context, args map[string]interface{}) (string, error) {
		argsJSON, _ := json.Marshal(args)

		timeout := time.Duration(def.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 30 * time.Second
		}

		client := &http.Client{Timeout: timeout}
		req, err := http.NewRequestWithContext(ctx, "POST", def.Command, bytes.NewReader(argsJSON))
		if err != nil {
			return "", fmt.Errorf("plugin %s: create request: %w", def.Name, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("plugin %s http: %w", def.Name, err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("plugin %s: HTTP %d: %s", def.Name, resp.StatusCode, string(body))
		}
		return string(body), nil
	}
}
