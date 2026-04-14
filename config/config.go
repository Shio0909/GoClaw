package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 全局配置结构
type Config struct {
	Server  ServerConfig            `yaml:"server"`
	Agent   AgentConfig             `yaml:"agent"`
	Gateway GatewayConfig           `yaml:"gateways"`
	Tools   ToolsConfig             `yaml:"tools"`
	MCP     map[string]MCPServer    `yaml:"mcp_servers"`
	RAG     RAGConfig               `yaml:"rag"`
}

type ServerConfig struct {
	Listen   string `yaml:"listen"`    // HTTP 监听地址，如 ":8080"
	LogLevel string `yaml:"log_level"` // 日志级别: debug, info, warn, error（默认 info）
	LogJSON  bool   `yaml:"log_json"`  // 是否使用 JSON 格式日志（默认 false = text）
}

type AgentConfig struct {
	Provider      string   `yaml:"provider"`
	APIKey        string   `yaml:"api_key"`
	BaseURL       string   `yaml:"base_url"`
	Model         string   `yaml:"model"`
	ContextLength int      `yaml:"context_length"`
	APIKeys       []string `yaml:"api_keys"` // 多 Key 轮换
	SimpleModel   string   `yaml:"simple_model"`
	SimpleProvider string  `yaml:"simple_provider"`
	SimpleBaseURL string   `yaml:"simple_base_url"`
	FallbackModel    string `yaml:"fallback_model"`    // 主模型失败时的备用模型
	FallbackProvider string `yaml:"fallback_provider"` // 备用模型 provider
	FallbackBaseURL  string `yaml:"fallback_base_url"` // 备用模型 base URL
	FallbackAPIKey   string `yaml:"fallback_api_key"`  // 备用模型 API Key（留空则复用主 Key）
	MaxStep       int      `yaml:"max_step"`        // Agent 最大工具调用步数（默认 25）
	ToolMaxBytes  int      `yaml:"tool_max_bytes"`  // 工具结果最大字节数（默认 30KB）
	SystemPrompt  string   `yaml:"system_prompt"`   // 自定义 system prompt（追加到默认 prompt）
	Temperature   *float32 `yaml:"temperature"`     // 采样温度（nil = 使用模型默认值）
	MaxTokens     int      `yaml:"max_tokens"`      // 最大输出 token 数（0 = 使用模型默认值）
	ReasoningEffort string `yaml:"reasoning_effort"` // 推理力度: low, medium, high（仅 o1/o3 等推理模型）
}

type GatewayConfig struct {
	QQ   *QQConfig   `yaml:"qq"`
	HTTP *HTTPConfig `yaml:"http"`
}

// HTTPConfig HTTP API 网关配置
type HTTPConfig struct {
	APIToken       string   `yaml:"api_token"`        // 可选 Bearer Token 认证
	CORS           []string `yaml:"cors_origins"`     // CORS 允许的域名，["*"] 为全部
	SessionTimeout int      `yaml:"session_timeout"`  // 会话超时（分钟），默认 30
	RequestTimeout int      `yaml:"request_timeout"`  // 请求超时（秒），默认 300
	SessionDir     string   `yaml:"session_dir"`      // 会话持久化目录，空则不持久化
	RateLimit      int      `yaml:"rate_limit"`       // 每分钟请求限制（0 = 不限制）
}

type QQConfig struct {
	Enabled     bool     `yaml:"enabled"`
	WebSocket   string   `yaml:"websocket"`
	SelfID      string   `yaml:"self_id"`
	Admins      []string `yaml:"admins"`
	StickersDir string   `yaml:"stickers_dir"`
	STT         STTConfig `yaml:"stt"`
}

type STTConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

type ToolsConfig struct {
	Sandbox    string `yaml:"sandbox"`
	TavilyKey  string `yaml:"tavily_key"`
	SmitheryKey string `yaml:"smithery_key"`
	SkillsDir  string `yaml:"skills_dir"`
	SkillNudge int    `yaml:"skill_nudge_interval"`
}

type MCPServer struct {
	Command   string            `yaml:"command"`
	Args      []string          `yaml:"args"`
	Env       map[string]string `yaml:"env"`
	Endpoint  string            `yaml:"endpoint"`
	Transport string            `yaml:"transport"`
	APIKey    string            `yaml:"api_key"`
	ToolNames []string          `yaml:"tool_names"`
}

// RAGConfig 检索增强生成配置
type RAGConfig struct {
	Providers []RAGProvider `yaml:"providers"`
}

type RAGProvider struct {
	Name    string `yaml:"name"`     // 显示名称
	Type    string `yaml:"type"`     // "http" (目前只支持 HTTP)
	BaseURL string `yaml:"base_url"` // API 端点 URL
	APIKey  string `yaml:"api_key"`  // 可选 Bearer token
}

// Load 加载配置，优先 YAML 文件，环境变量作为回退/覆盖
func Load(path string) (*Config, error) {
	cfg := &Config{}

	// 尝试读取 YAML 文件
	if data, err := os.ReadFile(path); err == nil {
		// 展开 ${ENV_VAR} 引用
		expanded := expandEnvVars(string(data))
		if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	// 环境变量回退（优先级：YAML 文件中的值 > 环境变量）
	// 但如果 YAML 字段为空，则从环境变量填充
	applyEnvFallback(cfg)

	// 设置默认值
	applyDefaults(cfg)

	return cfg, nil
}

// expandEnvVars 展开 ${VAR} 和 $VAR 引用
func expandEnvVars(s string) string {
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := match[2 : len(match)-1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match // 保留未解析的引用
	})
}

// applyEnvFallback 从环境变量填充空字段
func applyEnvFallback(cfg *Config) {
	// Server
	envStr(&cfg.Server.Listen, "GOCLAW_LISTEN")

	// Agent
	envStr(&cfg.Agent.Provider, "GOCLAW_PROVIDER")
	envStr(&cfg.Agent.Model, "GOCLAW_MODEL")
	envInt(&cfg.Agent.ContextLength, "GOCLAW_CONTEXT_LENGTH")
	envStr(&cfg.Agent.SimpleModel, "GOCLAW_SIMPLE_MODEL")
	envStr(&cfg.Agent.SimpleProvider, "GOCLAW_SIMPLE_PROVIDER")
	envStr(&cfg.Agent.SimpleBaseURL, "GOCLAW_SIMPLE_BASE_URL")
	envInt(&cfg.Agent.MaxStep, "GOCLAW_MAX_STEP")
	envInt(&cfg.Agent.ToolMaxBytes, "GOCLAW_TOOL_MAX_BYTES")

	// Provider-specific API keys
	if cfg.Agent.APIKey == "" {
		switch cfg.Agent.Provider {
		case "claude":
			envStr(&cfg.Agent.APIKey, "ANTHROPIC_API_KEY")
			envStr(&cfg.Agent.BaseURL, "ANTHROPIC_BASE_URL")
		case "minimax":
			envStr(&cfg.Agent.APIKey, "MINIMAX_API_KEY")
			envStr(&cfg.Agent.BaseURL, "MINIMAX_BASE_URL")
		case "mimo":
			envStr(&cfg.Agent.APIKey, "MIMO_API_KEY")
		case "siliconflow":
			envStr(&cfg.Agent.APIKey, "SILICONFLOW_API_KEY")
		default:
			envStr(&cfg.Agent.APIKey, "OPENAI_API_KEY")
			envStr(&cfg.Agent.BaseURL, "OPENAI_BASE_URL")
		}
	}

	// 多 Key 轮换
	if len(cfg.Agent.APIKeys) == 0 {
		if keys := os.Getenv("GOCLAW_API_KEYS"); keys != "" {
			for _, k := range strings.Split(keys, ",") {
				k = strings.TrimSpace(k)
				if k != "" {
					cfg.Agent.APIKeys = append(cfg.Agent.APIKeys, k)
				}
			}
		}
	}

	// Tools
	envStr(&cfg.Tools.Sandbox, "GOCLAW_SANDBOX")
	envStr(&cfg.Tools.TavilyKey, "TAVILY_API_KEY")
	envStr(&cfg.Tools.SmitheryKey, "SMITHERY_API_KEY")
	envStr(&cfg.Tools.SkillsDir, "GOCLAW_SKILLS_DIR")
	envInt(&cfg.Tools.SkillNudge, "GOCLAW_SKILL_NUDGE_INTERVAL")

	// QQ Gateway
	if cfg.Gateway.QQ == nil {
		qqWS := os.Getenv("GOCLAW_QQ_WS")
		if qqWS != "" {
			cfg.Gateway.QQ = &QQConfig{
				Enabled:   true,
				WebSocket: qqWS,
				SelfID:    os.Getenv("GOCLAW_QQ_SELF_ID"),
			}
			if admins := os.Getenv("GOCLAW_QQ_ADMINS"); admins != "" {
				cfg.Gateway.QQ.Admins = strings.Split(admins, ",")
			}
			envStr(&cfg.Gateway.QQ.StickersDir, "GOCLAW_STICKERS_DIR")
			cfg.Gateway.QQ.STT = STTConfig{
				BaseURL: os.Getenv("GOCLAW_STT_BASE_URL"),
				APIKey:  os.Getenv("GOCLAW_STT_API_KEY"),
				Model:   os.Getenv("GOCLAW_STT_MODEL"),
			}
		}
	}
}

// applyDefaults 设置合理默认值
func applyDefaults(cfg *Config) {
	if cfg.Agent.Provider == "" {
		cfg.Agent.Provider = "openai"
	}
	if cfg.Agent.ContextLength <= 0 {
		cfg.Agent.ContextLength = 128000
	}
	if cfg.Agent.MaxStep <= 0 {
		cfg.Agent.MaxStep = 25
	}
	if cfg.Agent.ToolMaxBytes <= 0 {
		cfg.Agent.ToolMaxBytes = 30 * 1024 // 30KB
	}
	if cfg.Tools.SkillsDir == "" {
		cfg.Tools.SkillsDir = "skills"
	}
	if cfg.Tools.SkillNudge <= 0 {
		cfg.Tools.SkillNudge = 8
	}

	// Provider-specific defaults
	switch cfg.Agent.Provider {
	case "minimax":
		if cfg.Agent.BaseURL == "" {
			cfg.Agent.BaseURL = "https://api.minimax.chat/v1"
		}
		if cfg.Agent.Model == "" {
			cfg.Agent.Model = "MiniMax-M2.7"
		}
	case "mimo":
		if cfg.Agent.BaseURL == "" {
			cfg.Agent.BaseURL = "https://api.xiaomimimo.com/v1"
		}
		if cfg.Agent.Model == "" {
			cfg.Agent.Model = "mimo-v2-pro"
		}
	case "siliconflow":
		if cfg.Agent.BaseURL == "" {
			cfg.Agent.BaseURL = "https://api.siliconflow.cn/v1"
		}
		if cfg.Agent.Model == "" {
			cfg.Agent.Model = "Pro/MiniMaxAI/MiniMax-M2.5"
		}
	case "claude":
		if cfg.Agent.Model == "" {
			cfg.Agent.Model = "claude-sonnet-4-20250514"
		}
	default: // openai
		if cfg.Agent.BaseURL == "" {
			cfg.Agent.BaseURL = "https://api.openai.com/v1"
		}
		if cfg.Agent.Model == "" {
			cfg.Agent.Model = "gpt-4o-mini"
		}
	}
}

func envStr(target *string, key string) {
	if *target == "" {
		if val := os.Getenv(key); val != "" {
			*target = val
		}
	}
}

func envInt(target *int, key string) {
	if *target == 0 {
		if val := os.Getenv(key); val != "" {
			if v, err := strconv.Atoi(val); err == nil {
				*target = v
			}
		}
	}
}
