package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/config"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
)

var requestCounter atomic.Int64

// HTTPServer 提供 RESTful API 的网关，支持 SSE 流式输出
type HTTPServer struct {
	addr     string
	agentCfg agent.Config
	registry *tools.Registry
	memStore *memory.Store

	sessions       sync.Map // sessionID -> *httpSession
	retryConfig    *agent.RetryConfig
	contextLength  int
	apiToken       string   // 可选的 Bearer Token 认证
	corsOrigins    []string // CORS 允许的域名
	sessionTimeout time.Duration
	requestTimeout time.Duration
	sessionStore   *SessionStore // 会话持久化（可选）
	startedAt      time.Time     // 服务启动时间
	chatCount      atomic.Int64  // 已处理的 chat 请求数
	rateLimiter    *RateLimiter  // 速率限制（可选）
	fallbackCfg    *agent.FallbackConfig // 模型回退（可选）
	activeConns    atomic.Int64  // 当前活跃请求数
	shutdownCh     chan struct{} // 触发优雅关闭
	configPath     string        // 配置文件路径（用于热重载）
	disabledTools  sync.Map         // toolName -> bool，运行时禁用的工具
	endpointStats  sync.Map         // endpoint -> *endpointStat，端点延迟统计
	pluginMgr      *tools.PluginManager // 插件管理器（可选）
	toolUsage      sync.Map             // toolName -> *toolUsageStats
	promptTemplates sync.Map            // name -> *promptTemplate
	eventSubs      sync.Map             // subID -> chan *serverEvent (SSE 订阅者)
	eventSubSeq    atomic.Int64         // 订阅者 ID 自增序列
	sessionTemplates sync.Map           // name -> *sessionTemplate
	adkCheckpointStore *agent.FileCheckPointStore // ADK 检查点存储

	server *http.Server
}

// serverEvent 服务器事件（用于 SSE 推送）
type serverEvent struct {
	Type      string      `json:"type"`
	Session   string      `json:"session,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp string      `json:"timestamp"`
}

// sessionCheckpoint 会话检查点
type sessionCheckpoint struct {
	Name      string            `json:"name"`
	History   []*schema.Message `json:"history"`
	CreatedAt string            `json:"created_at"`
}

type toolUsageStats struct {
	Calls    atomic.Int64
	Errors   atomic.Int64
	TotalMs  atomic.Int64 // 累计耗时毫秒
}

type promptTemplate struct {
	Name        string `json:"name"`
	Template    string `json:"template"`
	Description string `json:"description,omitempty"`
	Variables   []string `json:"variables,omitempty"`
}

// sessionTemplate 会话模板（预配置的会话设置）
type sessionTemplate struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	MessageQuota int               `json:"message_quota,omitempty"`
	CreatedAt    string            `json:"created_at"`
}

type httpSession struct {
	agent       *agent.Agent
	memMgr      *memory.Manager
	lastUsed    time.Time
	tags        map[string]bool // 会话标签
	customTTL   time.Duration   // 自定义存活时间（0 = 使用全局默认）
	locked      bool            // 锁定状态（锁定后禁止新消息）
	lockedBy    string          // 锁定者标识
	systemPromptOverride string // 会话级 system prompt 覆盖
	checkpoints []sessionCheckpoint // 命名检查点
	metadata    map[string]string   // 自定义元数据
	title       string              // 会话标题
	createdAt   time.Time           // 创建时间
	messageQuota int               // 消息配额（0 = 无限）
	messageCount int               // 已发送消息数
	starred     bool               // 是否已收藏
	archived    bool               // 是否已归档
}

type endpointStat struct {
	calls   atomic.Int64
	totalMs atomic.Int64
	errors  atomic.Int64
}

// HTTPServerConfig HTTP API 服务器配置
type HTTPServerConfig struct {
	Addr           string             // 监听地址，如 ":8080"
	AgentCfg       agent.Config
	Registry       *tools.Registry
	MemStore       *memory.Store
	RetryConfig    *agent.RetryConfig
	ContextLength  int
	APIToken       string   // 可选，设置后需 Bearer Token 认证
	CORSOrigins    []string // CORS 允许的域名，["*"] 为全部
	SessionTimeout int      // 会话超时（分钟），默认 30
	RequestTimeout int      // 请求超时（秒），默认 300
	SessionDir     string   // 会话持久化目录，空则不持久化
	RateLimit      int      // 每分钟请求限制（0 = 不限制）
	FallbackCfg    *agent.FallbackConfig // 模型回退配置（可选）
	ConfigPath     string                // 配置文件路径（用于热重载）
	PluginDir      string                // 插件目录（可选）
}

// 编译期检查 HTTPServer 实现 Gateway 接口
var _ Gateway = (*HTTPServer)(nil)

// NewHTTPServer 创建 HTTP API 网关
func NewHTTPServer(cfg HTTPServerConfig) *HTTPServer {
	ctxLen := cfg.ContextLength
	if ctxLen <= 0 {
		ctxLen = 128000
	}
	sessTimeout := time.Duration(cfg.SessionTimeout) * time.Minute
	if sessTimeout <= 0 {
		sessTimeout = 30 * time.Minute
	}
	reqTimeout := time.Duration(cfg.RequestTimeout) * time.Second
	if reqTimeout <= 0 {
		reqTimeout = 5 * time.Minute
	}

	var store *SessionStore
	if cfg.SessionDir != "" {
		var err error
		store, err = NewSessionStore(cfg.SessionDir)
		if err != nil {
			log.Printf("[HTTP] 会话持久化目录创建失败: %v (跳过持久化)", err)
		} else {
			log.Printf("[HTTP] 会话持久化: %s", cfg.SessionDir)
		}
	}

	srv := &HTTPServer{
		addr:           cfg.Addr,
		agentCfg:       cfg.AgentCfg,
		registry:       cfg.Registry,
		memStore:       cfg.MemStore,
		retryConfig:    cfg.RetryConfig,
		contextLength:  ctxLen,
		apiToken:       cfg.APIToken,
		corsOrigins:    cfg.CORSOrigins,
		sessionTimeout: sessTimeout,
		requestTimeout: reqTimeout,
		sessionStore:   store,
		shutdownCh:     make(chan struct{}),
		configPath:     cfg.ConfigPath,
	}

	// 从磁盘恢复会话
	if store != nil {
		srv.restoreSessions()
	}

	// ADK 检查点存储初始化
	adkDir := filepath.Join("memory_data", "adk_checkpoints")
	if cfg.SessionDir != "" {
		adkDir = filepath.Join(cfg.SessionDir, "adk_checkpoints")
	}
	adkStore, err := agent.NewFileCheckPointStore(adkDir)
	if err != nil {
		log.Printf("[HTTP] ADK 检查点存储初始化失败: %v", err)
	} else {
		srv.adkCheckpointStore = adkStore
		log.Printf("[HTTP] ADK 检查点存储: %s", adkDir)
	}

	// 速率限制
	if cfg.RateLimit > 0 {
		srv.rateLimiter = NewRateLimiter(cfg.RateLimit, time.Minute)
		log.Printf("[HTTP] 速率限制: %d 请求/分钟", cfg.RateLimit)
	}

	// 模型回退
	if cfg.FallbackCfg != nil && cfg.FallbackCfg.Model != "" {
		srv.fallbackCfg = cfg.FallbackCfg
		log.Printf("[HTTP] 模型回退: %s/%s", cfg.FallbackCfg.Provider, cfg.FallbackCfg.Model)
	}

	// 工具运行时禁用检查：让 Registry 感知 disabledTools
	srv.registry.SetDisabledChecker(func(name string) bool {
		_, disabled := srv.disabledTools.Load(name)
		return disabled
	})

	// 加载插件
	if cfg.PluginDir != "" {
		pm := tools.NewPluginManager(cfg.PluginDir)
		n, err := pm.LoadDir()
		if err != nil {
			log.Printf("[HTTP] 插件目录加载失败: %v", err)
		} else if n > 0 {
			pm.RegisterAll(srv.registry)
			log.Printf("[HTTP] 已加载 %d 个插件", n)
		}
		srv.pluginMgr = pm
	}

	return srv
}

func (s *HTTPServer) Name() string { return "http" }

// Shutdown 触发优雅关闭（可由 admin API 调用）
func (s *HTTPServer) Shutdown() {
	select {
	case s.shutdownCh <- struct{}{}:
	default:
	}
}

// ActiveConnections 返回当前活跃的 HTTP 请求数
func (s *HTTPServer) ActiveConnections() int64 {
	return s.activeConns.Load()
}

// Run 启动 HTTP 服务器，阻塞直到 ctx 取消
func (s *HTTPServer) Run(ctx context.Context) error {
	s.startedAt = time.Now()
	mux := http.NewServeMux()
	// GoClaw native API
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	mux.HandleFunc("GET /v1/chat/{session}", s.handleGetHistory)
	mux.HandleFunc("DELETE /v1/chat/{session}", s.handleDeleteSession)
	mux.HandleFunc("GET /v1/chat/{session}/export", s.handleExportSession)
	mux.HandleFunc("GET /v1/tools", s.handleListTools)
	mux.HandleFunc("GET /v1/memory/{session}", s.handleGetMemory)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("POST /v1/sessions/{session}/fork", s.handleForkSession)
	mux.HandleFunc("GET /v1/config", s.handleGetConfig)
	mux.HandleFunc("GET /v1/openapi.json", s.handleOpenAPISpec)
	mux.HandleFunc("POST /v1/config/reload", s.handleConfigReload)
	mux.HandleFunc("GET /v1/sessions/search", s.handleSessionSearch)
	mux.HandleFunc("PUT /v1/sessions/{session}/tags", s.handleSetTags)
	mux.HandleFunc("GET /v1/sessions/{session}/tags", s.handleGetTags)
	mux.HandleFunc("POST /v1/batch/chat", s.handleBatchChat)
	mux.HandleFunc("POST /v1/admin/gc", s.handleAdminGC)
	mux.HandleFunc("GET /v1/analytics", s.handleAnalytics)
	mux.HandleFunc("GET /v1/health/deep", s.handleDeepHealth)
	mux.HandleFunc("POST /v1/tools/{name}/disable", s.handleDisableTool)
	mux.HandleFunc("POST /v1/tools/{name}/enable", s.handleEnableTool)
	mux.HandleFunc("GET /v1/tools/disabled", s.handleListDisabledTools)
	// Plugin management
	mux.HandleFunc("GET /v1/plugins", s.handleListPlugins)
	mux.HandleFunc("POST /v1/plugins/reload", s.handleReloadPlugins)
	mux.HandleFunc("DELETE /v1/plugins/{name}", s.handleUnloadPlugin)
	// Cron / scheduled tasks
	// Session TTL
	// Tool aliases
	// Debug
	// Environment info
	mux.HandleFunc("GET /v1/env", s.handleEnvInfo)
	// Session rename
	mux.HandleFunc("POST /v1/sessions/{session}/rename", s.handleRenameSession)
	// Tool dry-run
	mux.HandleFunc("POST /v1/tools/{name}/dry-run", s.handleToolDryRun)
	// Session lock/unlock
	mux.HandleFunc("POST /v1/sessions/{session}/lock", s.handleLockSession)
	mux.HandleFunc("POST /v1/sessions/{session}/unlock", s.handleUnlockSession)
	// Cost estimation
	// Session stats
	mux.HandleFunc("GET /v1/sessions/{session}/stats", s.handleSessionStats)
	// Batch tool execution
	mux.HandleFunc("POST /v1/tools/batch", s.handleBatchTools)
	// Tool analytics
	// Prompt templates
	mux.HandleFunc("GET /v1/templates", s.handleListTemplates)
	mux.HandleFunc("POST /v1/templates", s.handleAddTemplate)
	mux.HandleFunc("DELETE /v1/templates/{name}", s.handleDeleteTemplate)
	// Session message search
	// Session message trim
	mux.HandleFunc("POST /v1/sessions/{session}/trim", s.handleTrimMessages)
	// System prompt override
	mux.HandleFunc("PUT /v1/sessions/{session}/system-prompt", s.handleSetSystemPrompt)
	mux.HandleFunc("GET /v1/sessions/{session}/system-prompt", s.handleGetSystemPrompt)
	// Session comparison
	// Conversation summary
	// Session import
	mux.HandleFunc("POST /v1/sessions/{session}/import", s.handleImportSession)
	// Message injection
	mux.HandleFunc("POST /v1/sessions/{session}/inject", s.handleInjectMessage)
	// Event SSE stream
	mux.HandleFunc("GET /v1/events", s.handleEventStream)
	// Session checkpoints
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint", s.handleCreateCheckpoint)
	mux.HandleFunc("GET /v1/sessions/{session}/checkpoints", s.handleListCheckpoints)
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint/restore", s.handleRestoreCheckpoint)
	// Message edit / delete / undo
	mux.HandleFunc("PUT /v1/sessions/{session}/messages/{index}", s.handleEditMessage)
	mux.HandleFunc("DELETE /v1/sessions/{session}/messages/{index}", s.handleDeleteMessage)
	mux.HandleFunc("POST /v1/sessions/{session}/undo", s.handleUndoMessage)
	// Session clone
	// Bulk session delete
	mux.HandleFunc("POST /v1/sessions/bulk-delete", s.handleBulkDeleteSessions)
	// Tool pipeline
	mux.HandleFunc("POST /v1/tools/pipeline", s.handleToolPipeline)
	// Session persist (manual save)
	mux.HandleFunc("POST /v1/sessions/{session}/save", s.handleSaveSession)
	// Fork at index
	// Message reactions
	// Session archive
	// Session history pagination
	mux.HandleFunc("GET /v1/sessions/{session}/messages", s.handleGetMessages)
	// Token count
	mux.HandleFunc("GET /v1/sessions/{session}/tokens", s.handleTokenCount)
	// Uptime
	// Session metadata
	mux.HandleFunc("GET /v1/sessions/{session}/meta", s.handleGetSessionMeta)
	mux.HandleFunc("PUT /v1/sessions/{session}/meta", s.handleSetSessionMeta)
	// Message bookmark
	// Session starring
	// Message pinning
	// Markdown export
	// Batch export
	mux.HandleFunc("POST /v1/sessions/export", s.handleBatchExport)
	// Conversation branching
	// Global message search
	mux.HandleFunc("GET /v1/search/messages", s.handleGlobalSearch)
	// Session merge
	// Auto-title
	// Session timeline
	// Message voting
	// Session categories
	// Message threading
	// Session sharing
	// Usage quotas
	// HTML export
	// Conversation tree view
	// Batch message operations
	// Session priority
	// CSV export
	// Bulk archive / unarchive
	// Message annotations
	// Session templates

	// Duplicate detection
	// Message diff
	// Agent turn tracking
	// Session comparison
	// Message word frequency
	// Session health score
	// Bulk tag operations
	// Message sentiment (simple)
	// System tracing config
	// Session export JSONL
	// Message count by role
	// Prompt preview
	// ──── Batch 5: Tool analytics, system info, session rename, YAML export, etc. ────
	mux.HandleFunc("GET /v1/sessions/{session}/tool-usage", s.handleToolUsage)

	mux.HandleFunc("GET /v1/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /v1/sessions/{session}/context-window", s.handleContextWindow)
	// ──── Batch 6: Session Groups, Webhook API, Rate Limit Info, etc. ────
	// ──── Batch 7: Analysis, Flow, Export, Auto-tag, etc. ────
	mux.HandleFunc("POST /v1/sessions/{session}/from-template", s.handleSessionFromTemplate)

	// ──── Batch 8: ADK Checkpoint/Resume (Eino v0.8.4 adk integration) ────
	mux.HandleFunc("GET /v1/adk/checkpoints", s.handleListADKCheckpoints)
	mux.HandleFunc("POST /v1/adk/checkpoints", s.handleSaveADKCheckpoint)
	mux.HandleFunc("GET /v1/adk/checkpoints/{key}", s.handleGetADKCheckpoint)
	mux.HandleFunc("DELETE /v1/adk/checkpoints/{key}", s.handleDeleteADKCheckpoint)
	// OpenAI-compatible endpoints
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	// WebSocket
	mux.HandleFunc("GET /v1/ws", s.handleWebSocket)

	// 中间件链：CORS → 速率限制 → 请求日志 → 认证
	var handler http.Handler = s.withAuth(s.withRequestLog(mux))
	if s.rateLimiter != nil {
		handler = s.rateLimiter.Middleware(handler)
	}
	handler = s.withCORS(handler)

	s.server = &http.Server{
		Addr:         s.addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: s.requestTimeout + 10*time.Second, // 留 10s buffer
		IdleTimeout:  120 * time.Second,
	}

	// 会话清理
	go s.cleanSessions(ctx)

	// 优雅关闭
	go func() {
		select {
		case <-ctx.Done():
		case <-s.shutdownCh:
		}
		log.Printf("[HTTP] 开始优雅关闭 (活跃连接: %d)", s.activeConns.Load())
		s.saveAllSessions()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.server.Shutdown(shutdownCtx)
	}()

	log.Printf("[HTTP] API 服务器启动: %s (CORS=%v, 会话超时=%v)", s.addr, s.corsOrigins, s.sessionTimeout)
	if err := s.server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// -------- 中间件 --------

// withAuth Bearer Token 认证（health 端点豁免）
func (s *HTTPServer) withAuth(next http.Handler) http.Handler {
	if s.apiToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.apiToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withCORS 处理跨域请求
func (s *HTTPServer) withCORS(next http.Handler) http.Handler {
	if len(s.corsOrigins) == 0 {
		return next
	}
	allowAll := len(s.corsOrigins) == 1 && s.corsOrigins[0] == "*"
	originSet := make(map[string]bool, len(s.corsOrigins))
	for _, o := range s.corsOrigins {
		originSet[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || originSet[origin]) {
			allowed := origin
			if allowAll {
				allowed = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRequestLog 记录每个请求的方法、路径和耗时，并注入 X-Request-ID
func (s *HTTPServer) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.activeConns.Add(1)
		defer s.activeConns.Add(-1)

		reqID := requestCounter.Add(1)
		xRequestID := r.Header.Get("X-Request-ID")
		if xRequestID == "" {
			xRequestID = fmt.Sprintf("goclaw-%d-%d", s.startedAt.Unix(), reqID)
		}
		w.Header().Set("X-Request-ID", xRequestID)
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)

		elapsed := time.Since(start)
		log.Printf("[HTTP] #%d %s %s %s → %d (%v)", reqID, xRequestID, r.Method, r.URL.Path, rw.status, elapsed.Round(time.Millisecond))

		// 端点延迟统计
		key := r.Method + " " + r.URL.Path
		val, _ := s.endpointStats.LoadOrStore(key, &endpointStat{})
		stat := val.(*endpointStat)
		stat.calls.Add(1)
		stat.totalMs.Add(elapsed.Milliseconds())
		if rw.status >= 400 {
			stat.errors.Add(1)
		}
	})
}

// responseWriter 包装 http.ResponseWriter 以捕获 status code
type responseWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.wrote {
		rw.status = code
		rw.wrote = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// -------- API Handlers --------

// chatRequest POST /v1/chat 请求体
type chatRequest struct {
	Session string `json:"session"` // 会话 ID（必填）
	Message string `json:"message"` // 用户消息
	Stream  bool   `json:"stream"`  // 是否使用 SSE 流式输出
}

// chatResponse 非流式回复
type chatResponse struct {
	Session string `json:"session"`
	Content string `json:"content"`
}

func (s *HTTPServer) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Session == "" || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session and message are required"})
		return
	}

	ag := s.getOrCreateSession(req.Session)

	// 检查会话锁定状态
	if val, ok := s.sessions.Load(req.Session); ok {
		if sess, ok := val.(*httpSession); ok {
			if sess.locked {
				writeJSON(w, http.StatusLocked, map[string]string{
					"error":     "session is locked",
					"locked_by": sess.lockedBy,
				})
				return
			}
			// 检查消息配额
			if sess.messageQuota > 0 && sess.messageCount >= sess.messageQuota {
				writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
					"error": "message quota exceeded",
					"quota": sess.messageQuota,
					"used":  sess.messageCount,
				})
				return
			}
			sess.messageCount++
			// 应用会话级 system prompt 覆盖
			if sess.systemPromptOverride != "" {
				ag.SetExtraSystemPrompt(sess.systemPromptOverride)
			}
		}
	}

	s.chatCount.Add(1)

	if req.Stream {
		s.handleStreamChat(w, r, ag, req)
		return
	}

	// 非流式
	resp, err := ag.Run(r.Context(), req.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, chatResponse{Session: req.Session, Content: resp})
}

func (s *HTTPServer) handleStreamChat(w http.ResponseWriter, r *http.Request, ag *agent.Agent, req chatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	stream, err := ag.RunStream(r.Context(), req.Message)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	filter := &agent.ThinkFilter{}
	var fullContent strings.Builder

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
			flusher.Flush()
			break
		}
		if msg != nil && msg.Content != "" {
			filtered := filter.Process(msg.Content)
			if filtered != "" {
				data, _ := json.Marshal(map[string]string{"content": filtered})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				fullContent.WriteString(filtered)
			}
		}
	}

	if remaining := filter.Flush(); remaining != "" {
		data, _ := json.Marshal(map[string]string{"content": remaining})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fullContent.WriteString(remaining)
	}

	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	if fullContent.Len() > 0 {
		ag.AppendAssistantMessage(r.Context(), fullContent.String())
	}
}

func (s *HTTPServer) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	sess.lastUsed = time.Now()

	writeJSON(w, http.StatusOK, map[string]string{
		"session": sessionID,
		"status":  "active",
	})
}

func (s *HTTPServer) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	if _, ok := s.sessions.Load(sessionID); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	s.sessions.Delete(sessionID)
	if s.sessionStore != nil {
		_ = s.sessionStore.Delete(sessionID)
	}
	s.emitEvent("session.deleted", sessionID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"message": "session deleted"})
}

func (s *HTTPServer) handleListTools(w http.ResponseWriter, r *http.Request) {
	names := s.registry.Names()
	type toolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Disabled    bool   `json:"disabled,omitempty"`
	}
	toolList := make([]toolInfo, 0, len(names))
	for _, name := range names {
		t, _ := s.registry.Get(name)
		if t != nil {
			_, disabled := s.disabledTools.Load(name)
			toolList = append(toolList, toolInfo{Name: t.Name, Description: t.Description, Disabled: disabled})
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tools": toolList, "count": len(toolList)})
}

func (s *HTTPServer) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	_ = sessionID // 记忆目前是全局的，sessionID 用于未来扩展

	soul, _ := s.memStore.ReadSoul()
	user, _ := s.memStore.ReadUser()
	mem, _ := s.memStore.ReadMemory()
	logs, _ := s.memStore.ReadTodayLogs()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"soul_length":   len(soul),
		"user_length":   len(user),
		"memory_length": len(mem),
		"today_logs":    len(logs),
	})
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	sessionCount := 0
	s.sessions.Range(func(_, _ any) bool {
		sessionCount++
		return true
	})

	resp := map[string]interface{}{
		"status":            "ok",
		"gateway":           s.Name(),
		"provider":          s.agentCfg.Provider,
		"model":             s.agentCfg.Model,
		"tools":             len(s.registry.Names()),
		"uptime_seconds":    int(time.Since(s.startedAt).Seconds()),
		"active_sessions":   sessionCount,
		"active_connections": s.activeConns.Load(),
		"total_chats":       s.chatCount.Load(),
	}
	if s.fallbackCfg != nil && s.fallbackCfg.Model != "" {
		resp["fallback_model"] = s.fallbackCfg.Model
	}
	if s.rateLimiter != nil {
		resp["rate_limit_enabled"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	sessionCount := 0
	s.sessions.Range(func(_, _ any) bool {
		sessionCount++
		return true
	})

	uptime := time.Since(s.startedAt)
	totalRequests := requestCounter.Load()
	totalChats := s.chatCount.Load()
	toolsLoaded := len(s.registry.Names())
	activeConns := s.activeConns.Load()

	// Prometheus text format
	if r.URL.Query().Get("format") == "prometheus" {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(w, "# HELP goclaw_uptime_seconds Time since server start in seconds.\n")
		fmt.Fprintf(w, "# TYPE goclaw_uptime_seconds gauge\n")
		fmt.Fprintf(w, "goclaw_uptime_seconds %d\n", int(uptime.Seconds()))
		fmt.Fprintf(w, "# HELP goclaw_requests_total Total HTTP requests processed.\n")
		fmt.Fprintf(w, "# TYPE goclaw_requests_total counter\n")
		fmt.Fprintf(w, "goclaw_requests_total %d\n", totalRequests)
		fmt.Fprintf(w, "# HELP goclaw_chats_total Total chat messages processed.\n")
		fmt.Fprintf(w, "# TYPE goclaw_chats_total counter\n")
		fmt.Fprintf(w, "goclaw_chats_total %d\n", totalChats)
		fmt.Fprintf(w, "# HELP goclaw_active_sessions Number of active sessions.\n")
		fmt.Fprintf(w, "# TYPE goclaw_active_sessions gauge\n")
		fmt.Fprintf(w, "goclaw_active_sessions %d\n", sessionCount)
		fmt.Fprintf(w, "# HELP goclaw_active_connections Number of in-flight HTTP requests.\n")
		fmt.Fprintf(w, "# TYPE goclaw_active_connections gauge\n")
		fmt.Fprintf(w, "goclaw_active_connections %d\n", activeConns)
		fmt.Fprintf(w, "# HELP goclaw_tools_loaded Number of tools loaded.\n")
		fmt.Fprintf(w, "# TYPE goclaw_tools_loaded gauge\n")
		fmt.Fprintf(w, "goclaw_tools_loaded %d\n", toolsLoaded)

		// Per-tool metrics
		for _, snap := range tools.GetGlobalToolStats().Snapshot() {
			fmt.Fprintf(w, "goclaw_tool_calls_total{tool=%q} %d\n", snap.Name, snap.Calls)
			fmt.Fprintf(w, "goclaw_tool_errors_total{tool=%q} %d\n", snap.Name, snap.Errors)
			if snap.Calls > 0 {
				fmt.Fprintf(w, "goclaw_tool_avg_duration_ms{tool=%q} %.1f\n", snap.Name, snap.AvgMs)
			}
		}
		return
	}

	// JSON format (default)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_seconds":    int(uptime.Seconds()),
		"uptime_human":      uptime.Round(time.Second).String(),
		"total_requests":    totalRequests,
		"total_chats":       totalChats,
		"active_sessions":   sessionCount,
		"active_connections": activeConns,
		"tools_loaded":      toolsLoaded,
		"model":             s.agentCfg.Model,
		"provider":          s.agentCfg.Provider,
		"tool_stats":        tools.GetGlobalToolStats().Summary(),
	})
}

// handleListSessions 列出所有活跃会话
func (s *HTTPServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	type sessionInfo struct {
		ID           string   `json:"id"`
		MessageCount int      `json:"message_count"`
		LastUsed     string   `json:"last_used"`
		IdleSeconds  int      `json:"idle_seconds"`
		Tags         []string `json:"tags,omitempty"`
	}

	// 可选 tag 过滤：GET /v1/sessions?tag=xxx
	filterTag := r.URL.Query().Get("tag")

	var sessions []sessionInfo
	s.sessions.Range(func(key, val any) bool {
		id := key.(string)
		sess := val.(*httpSession)

		if filterTag != "" && (sess.tags == nil || !sess.tags[filterTag]) {
			return true
		}

		idle := time.Since(sess.lastUsed)
		info := sessionInfo{
			ID:           id,
			MessageCount: len(sess.agent.GetHistory()),
			LastUsed:     sess.lastUsed.Format(time.RFC3339),
			IdleSeconds:  int(idle.Seconds()),
		}
		if len(sess.tags) > 0 {
			for t := range sess.tags {
				info.Tags = append(info.Tags, t)
			}
		}
		sessions = append(sessions, info)
		return true
	})

	if sessions == nil {
		sessions = []sessionInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":    len(sessions),
		"sessions": sessions,
	})
}

// handleForkSession POST /v1/sessions/{session}/fork — 克隆会话到新 ID
func (s *HTTPServer) handleForkSession(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("session")
	val, ok := s.sessions.Load(sourceID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sourceSess := val.(*httpSession)

	var req struct {
		NewSession string `json:"new_session"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewSession == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new_session is required"})
		return
	}

	if _, exists := s.sessions.Load(req.NewSession); exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "target session already exists"})
		return
	}

	// 复制对话历史到新会话
	newAgent := s.getOrCreateSession(req.NewSession)
	history := sourceSess.agent.GetHistory()
	copied := make([]*schema.Message, len(history))
	copy(copied, history)
	newAgent.SetHistory(copied)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"source":        sourceID,
		"new_session":   req.NewSession,
		"messages_copied": len(copied),
	})
}

// handleGetConfig GET /v1/config — 返回脱敏后的运行时配置
func (s *HTTPServer) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	maskKey := func(key string) string {
		if len(key) <= 8 {
			return "***"
		}
		return key[:4] + "..." + key[len(key)-4:]
	}

	cfg := map[string]interface{}{
		"provider": s.agentCfg.Provider,
		"model":    s.agentCfg.Model,
		"base_url": s.agentCfg.BaseURL,
		"api_key":  maskKey(s.agentCfg.APIKey),
		"max_step": s.agentCfg.MaxStep,
	}
	if s.agentCfg.Temperature != nil {
		cfg["temperature"] = *s.agentCfg.Temperature
	}
	if s.agentCfg.MaxTokens > 0 {
		cfg["max_tokens"] = s.agentCfg.MaxTokens
	}
	if s.agentCfg.ReasoningEffort != "" {
		cfg["reasoning_effort"] = s.agentCfg.ReasoningEffort
	}

	features := map[string]bool{
		"rate_limit":  s.rateLimiter != nil,
		"persistence": s.sessionStore != nil,
	}
	if s.fallbackCfg != nil && s.fallbackCfg.Model != "" {
		features["fallback"] = true
		cfg["fallback_model"] = s.fallbackCfg.Model
		cfg["fallback_provider"] = s.fallbackCfg.Provider
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"config":   cfg,
		"features": features,
		"tools":    len(s.registry.Names()),
	})
}

// handleConfigReload POST /v1/config/reload — 热重载配置文件
func (s *HTTPServer) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no config path set"})
		return
	}

	newCfg, err := config.Load(s.configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("reload failed: %v", err),
		})
		return
	}

	// 记录变更
	changes := map[string]interface{}{}
	if newCfg.Agent.Model != s.agentCfg.Model {
		changes["model"] = map[string]string{"old": s.agentCfg.Model, "new": newCfg.Agent.Model}
		s.agentCfg.Model = newCfg.Agent.Model
	}
	if newCfg.Agent.MaxStep != s.agentCfg.MaxStep {
		changes["max_step"] = map[string]int{"old": s.agentCfg.MaxStep, "new": newCfg.Agent.MaxStep}
		s.agentCfg.MaxStep = newCfg.Agent.MaxStep
	}
	if newCfg.Agent.MaxTokens != s.agentCfg.MaxTokens {
		changes["max_tokens"] = map[string]int{"old": s.agentCfg.MaxTokens, "new": newCfg.Agent.MaxTokens}
		s.agentCfg.MaxTokens = newCfg.Agent.MaxTokens
	}
	if newCfg.Agent.Temperature != nil && s.agentCfg.Temperature != nil {
		if *newCfg.Agent.Temperature != *s.agentCfg.Temperature {
			changes["temperature"] = map[string]float32{"old": *s.agentCfg.Temperature, "new": *newCfg.Agent.Temperature}
			s.agentCfg.Temperature = newCfg.Agent.Temperature
		}
	} else if newCfg.Agent.Temperature != nil && s.agentCfg.Temperature == nil {
		changes["temperature"] = map[string]interface{}{"old": nil, "new": *newCfg.Agent.Temperature}
		s.agentCfg.Temperature = newCfg.Agent.Temperature
	}
	if newCfg.Agent.ReasoningEffort != s.agentCfg.ReasoningEffort {
		changes["reasoning_effort"] = map[string]string{"old": s.agentCfg.ReasoningEffort, "new": newCfg.Agent.ReasoningEffort}
		s.agentCfg.ReasoningEffort = newCfg.Agent.ReasoningEffort
	}
	if newCfg.Agent.SystemPrompt != s.agentCfg.SystemPrompt {
		changes["system_prompt"] = "updated"
		s.agentCfg.SystemPrompt = newCfg.Agent.SystemPrompt
	}

	log.Printf("[HTTP] 配置热重载: %d 项变更", len(changes))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"reloaded": true,
		"changes":  changes,
	})
}

// handleSessionSearch GET /v1/sessions/search?q=keyword&limit=20 — 搜索会话内容
func (s *HTTPServer) handleSessionSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter is required"})
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}

	queryLower := strings.ToLower(query)

	type matchResult struct {
		SessionID    string `json:"session_id"`
		MessageCount int    `json:"message_count"`
		MatchCount   int    `json:"match_count"`
		Snippet      string `json:"snippet"`
		LastUsed     string `json:"last_used"`
	}

	var results []matchResult
	s.sessions.Range(func(key, val any) bool {
		if len(results) >= limit {
			return false
		}

		id := key.(string)
		sess := val.(*httpSession)
		history := sess.agent.GetHistory()

		matchCount := 0
		snippet := ""

		for _, msg := range history {
			content := msg.Content
			if content == "" {
				continue
			}
			contentLower := strings.ToLower(content)
			if idx := strings.Index(contentLower, queryLower); idx >= 0 {
				matchCount++
				if snippet == "" {
					// 提取匹配上下文（前后各 50 字符）
					start := idx - 50
					if start < 0 {
						start = 0
					}
					end := idx + len(query) + 50
					if end > len(content) {
						end = len(content)
					}
					snippet = content[start:end]
					if start > 0 {
						snippet = "..." + snippet
					}
					if end < len(content) {
						snippet = snippet + "..."
					}
				}
			}
		}

		if matchCount > 0 {
			results = append(results, matchResult{
				SessionID:    id,
				MessageCount: len(history),
				MatchCount:   matchCount,
				Snippet:      snippet,
				LastUsed:     sess.lastUsed.Format(time.RFC3339),
			})
		}

		return true
	})

	if results == nil {
		results = []matchResult{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query":   query,
		"count":   len(results),
		"results": results,
	})
}

func (s *HTTPServer) handleExportSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	history := sess.agent.GetHistory()

	format := r.URL.Query().Get("format")
	if format == "markdown" || format == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.md", sessionID))
		fmt.Fprintf(w, "# Conversation: %s\n\n", sessionID)
		for _, msg := range history {
			role := string(msg.Role)
			switch msg.Role {
			case "user":
				role = "🧑 User"
			case "assistant":
				role = "🤖 Assistant"
			case "tool":
				role = "🔧 Tool"
			case "system":
				role = "⚙️ System"
			}
			fmt.Fprintf(w, "## %s\n\n%s\n\n---\n\n", role, msg.Content)
		}
		return
	}

	if format == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.html", sessionID))
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font-family:system-ui,sans-serif;max-width:800px;margin:0 auto;padding:20px;background:#f5f5f5}
.msg{margin:12px 0;padding:12px 16px;border-radius:8px;line-height:1.6}
.user{background:#dcf8c6;margin-left:60px}.assistant{background:#fff;margin-right:60px}
.tool{background:#e8f4fd;font-family:monospace;font-size:13px}.system{background:#fff3cd;font-size:13px}
.role{font-weight:bold;margin-bottom:4px;font-size:13px;color:#666}
pre{background:#2d2d2d;color:#f8f8f2;padding:12px;border-radius:4px;overflow-x:auto}
h1{text-align:center;color:#333}</style></head><body>`, sessionID)
		fmt.Fprintf(w, "<h1>%s</h1>", sessionID)
		for _, msg := range history {
			cssClass := string(msg.Role)
			roleLabel := string(msg.Role)
			switch msg.Role {
			case "user":
				roleLabel = "🧑 User"
			case "assistant":
				roleLabel = "🤖 Assistant"
			case "tool":
				roleLabel = "🔧 Tool"
			case "system":
				roleLabel = "⚙️ System"
			}
			// 简单 HTML 转义
			content := strings.ReplaceAll(msg.Content, "&", "&amp;")
			content = strings.ReplaceAll(content, "<", "&lt;")
			content = strings.ReplaceAll(content, ">", "&gt;")
			content = strings.ReplaceAll(content, "\n", "<br>")
			fmt.Fprintf(w, `<div class="msg %s"><div class="role">%s</div>%s</div>`, cssClass, roleLabel, content)
		}
		fmt.Fprintf(w, "</body></html>")
		return
	}

	// 默认 JSON
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":       sessionID,
		"message_count": len(history),
		"messages":      history,
	})
}

// -------- OpenAI-Compatible Endpoints --------

// openaiMessage OpenAI 格式的消息
type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiRequest OpenAI /v1/chat/completions 请求格式
type openaiRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

// openaiChoice OpenAI 响应中的选项
type openaiChoice struct {
	Index        int            `json:"index"`
	Message      *openaiMessage `json:"message,omitempty"`
	Delta        *openaiMessage `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

// openaiUsage Token 用量
type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// openaiResponse OpenAI /v1/chat/completions 响应格式
type openaiResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req openaiRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]string{"message": "invalid json: " + err.Error(), "type": "invalid_request_error"},
		})
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]string{"message": "messages is required", "type": "invalid_request_error"},
		})
		return
	}

	// 提取最后一条用户消息
	var userMsg string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userMsg = req.Messages[i].Content
			break
		}
	}
	if userMsg == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]string{"message": "no user message found", "type": "invalid_request_error"},
		})
		return
	}

	// 使用 model 字段作为 session ID（简单映射，保证同一 "model" 复用会话）
	sessionID := fmt.Sprintf("openai-%s", hashStr(fmt.Sprintf("%v", req.Messages[:len(req.Messages)-1])))
	ag := s.getOrCreateSession(sessionID)
	s.chatCount.Add(1)

	respID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

	if req.Stream {
		s.handleOpenAIStream(w, r, ag, userMsg, respID)
		return
	}

	// 非流式
	result, err := ag.Run(r.Context(), userMsg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": map[string]string{"message": err.Error(), "type": "server_error"},
		})
		return
	}

	stop := "stop"
	writeJSON(w, http.StatusOK, openaiResponse{
		ID:      respID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   s.agentCfg.Model,
		Choices: []openaiChoice{{
			Index:        0,
			Message:      &openaiMessage{Role: "assistant", Content: result},
			FinishReason: &stop,
		}},
		Usage: &openaiUsage{
			PromptTokens:     len(userMsg) / 4, // 粗略估算
			CompletionTokens: len(result) / 4,
			TotalTokens:      (len(userMsg) + len(result)) / 4,
		},
	})
}

func (s *HTTPServer) handleOpenAIStream(w http.ResponseWriter, r *http.Request, ag *agent.Agent, userMsg, respID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": map[string]string{"message": "streaming not supported", "type": "server_error"},
		})
		return
	}

	stream, err := ag.RunStream(r.Context(), userMsg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": map[string]string{"message": err.Error(), "type": "server_error"},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	filter := &agent.ThinkFilter{}
	var fullContent strings.Builder

	sendChunk := func(content string, finish *string) {
		chunk := openaiResponse{
			ID:      respID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   s.agentCfg.Model,
			Choices: []openaiChoice{{
				Index:        0,
				Delta:        &openaiMessage{Role: "assistant", Content: content},
				FinishReason: finish,
			}},
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			stop := "error"
			sendChunk("", &stop)
			break
		}
		if msg != nil && msg.Content != "" {
			filtered := filter.Process(msg.Content)
			if filtered != "" {
				sendChunk(filtered, nil)
				fullContent.WriteString(filtered)
			}
		}
	}

	if remaining := filter.Flush(); remaining != "" {
		sendChunk(remaining, nil)
		fullContent.WriteString(remaining)
	}

	stop := "stop"
	sendChunk("", &stop)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

	if fullContent.Len() > 0 {
		ag.AppendAssistantMessage(r.Context(), fullContent.String())
	}
}

// handleModels 返回 OpenAI /v1/models 兼容的模型列表
func (s *HTTPServer) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"object": "list",
		"data": []map[string]interface{}{
			{
				"id":       s.agentCfg.Model,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "goclaw",
			},
		},
	})
}

// hashStr 简单哈希，用于生成 session ID
func hashStr(s string) string {
	h := uint64(0)
	for _, c := range s {
		h = h*31 + uint64(c)
	}
	return fmt.Sprintf("%x", h)
}

// -------- WebSocket --------

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // CORS 已由中间件处理
	},
}

type wsMessage struct {
	Type    string `json:"type"`    // "chat", "clear", "ping"
	Session string `json:"session"` // 会话 ID
	Message string `json:"message"` // 用户消息
}

type wsResponse struct {
	Type    string `json:"type"`              // "chunk", "done", "error", "pong"
	Content string `json:"content,omitempty"`
	Session string `json:"session,omitempty"`
}

func (s *HTTPServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] 升级失败: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("[WS] 新连接: %s", r.RemoteAddr)

	conn.SetReadLimit(64 * 1024)
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 心跳
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[WS] 读取错误: %v", err)
			}
			return
		}

		var msg wsMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			s.wsWrite(conn, wsResponse{Type: "error", Content: "invalid json"})
			continue
		}

		switch msg.Type {
		case "ping":
			s.wsWrite(conn, wsResponse{Type: "pong"})
		case "clear":
			if msg.Session != "" {
				ag := s.getOrCreateSession(msg.Session)
				ag.ClearHistory()
				s.wsWrite(conn, wsResponse{Type: "done", Session: msg.Session, Content: "history cleared"})
			}
		case "chat", "":
			if msg.Session == "" {
				msg.Session = fmt.Sprintf("ws-%d", time.Now().UnixNano())
			}
			s.handleWSChat(conn, msg)
		default:
			s.wsWrite(conn, wsResponse{Type: "error", Content: "unknown message type: " + msg.Type})
		}
	}
}

func (s *HTTPServer) handleWSChat(conn *websocket.Conn, msg wsMessage) {
	ag := s.getOrCreateSession(msg.Session)
	s.chatCount.Add(1)

	stream, err := ag.RunStream(context.Background(), msg.Message)
	if err != nil {
		s.wsWrite(conn, wsResponse{Type: "error", Content: err.Error(), Session: msg.Session})
		return
	}

	filter := &agent.ThinkFilter{}
	for {
		chunk, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			s.wsWrite(conn, wsResponse{Type: "error", Content: err.Error(), Session: msg.Session})
			return
		}
		if chunk != nil && chunk.Content != "" {
			filtered := filter.Process(chunk.Content)
			if filtered != "" {
				s.wsWrite(conn, wsResponse{Type: "chunk", Content: filtered, Session: msg.Session})
			}
		}
	}
	s.wsWrite(conn, wsResponse{Type: "done", Session: msg.Session})
}

func (s *HTTPServer) wsWrite(conn *websocket.Conn, resp wsResponse) {
	data, _ := json.Marshal(resp)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	conn.WriteMessage(websocket.TextMessage, data)
}

// -------- 会话管理 --------

func (s *HTTPServer) getOrCreateSession(id string) *agent.Agent {
	if val, ok := s.sessions.Load(id); ok {
		sess := val.(*httpSession)
		sess.lastUsed = time.Now()
		return sess.agent
	}

	memMgr := memory.NewManager(s.memStore, 10)
	llmCaller := func(ctx context.Context, sys, user string) (string, error) {
		tempAgent := agent.NewAgent(s.agentCfg, tools.NewRegistry(), memory.NewManager(s.memStore, 999))
		return tempAgent.Run(ctx, user)
	}
	memMgr.SetLLMCaller(llmCaller)

	ag := agent.NewAgent(s.agentCfg, s.registry, memMgr)
	compressor := agent.NewCompressor(agent.CompressorConfig{
		ContextLength: s.contextLength,
	}, llmCaller)
	ag.SetCompressor(compressor)

	if s.retryConfig != nil {
		ag.SetRetryConfig(s.retryConfig)
	}
	if s.fallbackCfg != nil {
		ag.SetFallbackConfig(s.fallbackCfg)
	}

	s.sessions.Store(id, &httpSession{
		agent:     ag,
		memMgr:    memMgr,
		lastUsed:  time.Now(),
		createdAt: time.Now(),
	})
	return ag
}

func (s *HTTPServer) cleanSessions(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sessions.Range(func(key, val any) bool {
				sess := val.(*httpSession)
				ttl := s.sessionTimeout
				if sess.customTTL > 0 {
					ttl = sess.customTTL
				}
				if time.Since(sess.lastUsed) > ttl {
					s.saveSession(key.(string), sess) // 过期前持久化
					s.sessions.Delete(key)
					log.Printf("[HTTP] 清理过期会话: %s", key)
				}
				return true
			})
			s.saveAllSessions() // 定期全量保存
		case <-ctx.Done():
			return
		}
	}
}

// restoreSessions 从磁盘恢复所有会话
func (s *HTTPServer) restoreSessions() {
	if s.sessionStore == nil {
		return
	}
	snapshots, err := s.sessionStore.LoadAll()
	if err != nil {
		log.Printf("[HTTP] 恢复会话失败: %v", err)
		return
	}
	restored := 0
	for _, snap := range snapshots {
		if time.Since(snap.LastUsed) > s.sessionTimeout {
			_ = s.sessionStore.Delete(snap.ID)
			continue
		}
		if len(snap.History) == 0 {
			_ = s.sessionStore.Delete(snap.ID)
			continue
		}
		ag := s.getOrCreateSession(snap.ID)
		ag.SetHistory(snap.History)
		restored++
	}
	if restored > 0 {
		log.Printf("[HTTP] 恢复了 %d 个会话", restored)
	}
}

// saveAllSessions 保存所有活跃会话到磁盘
func (s *HTTPServer) saveAllSessions() {
	if s.sessionStore == nil {
		return
	}
	s.sessions.Range(func(key, val any) bool {
		sess := val.(*httpSession)
		s.saveSession(key.(string), sess)
		return true
	})
}

// saveSession 保存单个会话到磁盘
func (s *HTTPServer) saveSession(id string, sess *httpSession) {
	if s.sessionStore == nil {
		return
	}
	history := sess.agent.GetHistory()
	if len(history) == 0 {
		return
	}
	snap := &SessionSnapshot{
		ID:       id,
		History:  history,
		SavedAt:  time.Now(),
		LastUsed: sess.lastUsed,
	}
	if err := s.sessionStore.Save(snap); err != nil {
		log.Printf("[HTTP] 保存会话失败 %s: %v", id, err)
	}
}

// -------- Session Tags & Annotations --------

// handleSetTags PUT /v1/sessions/{session}/tags — 添加标签
func (s *HTTPServer) handleSetTags(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var req struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(req.Tags) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tags is required"})
		return
	}

	if sess.tags == nil {
		sess.tags = make(map[string]bool)
	}
	for _, t := range req.Tags {
		if t = strings.TrimSpace(t); t != "" {
			sess.tags[t] = true
		}
	}

	var tags []string
	for t := range sess.tags {
		tags = append(tags, t)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sessionID,
		"tags":    tags,
	})
}

// handleGetTags GET /v1/sessions/{session}/tags — 获取标签
func (s *HTTPServer) handleGetTags(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var tags []string
	for t := range sess.tags {
		tags = append(tags, t)
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sessionID,
		"tags":    tags,
	})
}

// -------- Batch Operations --------

// handleBatchChat POST /v1/batch/chat — 批量对多个会话发送消息
func (s *HTTPServer) handleBatchChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sessions []string `json:"sessions"`
		Message  string   `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(req.Sessions) == 0 || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessions and message are required"})
		return
	}
	if len(req.Sessions) > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 20 sessions per batch"})
		return
	}

	type batchResult struct {
		Session string `json:"session"`
		Content string `json:"content,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	results := make([]batchResult, len(req.Sessions))
	var wg sync.WaitGroup
	for i, sid := range req.Sessions {
		wg.Add(1)
		go func(idx int, sessionID string) {
			defer wg.Done()
			ag := s.getOrCreateSession(sessionID)
			s.chatCount.Add(1)
			resp, err := ag.Run(r.Context(), req.Message)
			if err != nil {
				results[idx] = batchResult{Session: sessionID, Error: err.Error()}
			} else {
				results[idx] = batchResult{Session: sessionID, Content: resp}
			}
		}(i, sid)
	}
	wg.Wait()

	succeeded := 0
	for _, res := range results {
		if res.Error == "" {
			succeeded++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":     len(results),
		"succeeded": succeeded,
		"failed":    len(results) - succeeded,
		"results":   results,
	})
}

// -------- Admin & Analytics --------

// handleAdminGC POST /v1/admin/gc — 清理空闲/过期会话
func (s *HTTPServer) handleAdminGC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxIdleMinutes int `json:"max_idle_minutes"` // 0 = 使用默认 sessionTimeout
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	threshold := s.sessionTimeout
	if req.MaxIdleMinutes > 0 {
		threshold = time.Duration(req.MaxIdleMinutes) * time.Minute
	}

	var cleaned []string
	s.sessions.Range(func(key, val any) bool {
		id := key.(string)
		sess := val.(*httpSession)
		if time.Since(sess.lastUsed) > threshold {
			s.saveSession(id, sess)
			s.sessions.Delete(key)
			cleaned = append(cleaned, id)
		}
		return true
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cleaned":          len(cleaned),
		"cleaned_sessions": cleaned,
		"threshold":        threshold.String(),
	})
}

// handleAnalytics GET /v1/analytics — 运行时分析数据
func (s *HTTPServer) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	sessionCount := 0
	totalMessages := 0
	var oldestIdle, newestIdle time.Duration
	tagCounts := map[string]int{}

	s.sessions.Range(func(key, val any) bool {
		sessionCount++
		sess := val.(*httpSession)
		totalMessages += len(sess.agent.GetHistory())
		idle := time.Since(sess.lastUsed)
		if sessionCount == 1 || idle > oldestIdle {
			oldestIdle = idle
		}
		if sessionCount == 1 || idle < newestIdle {
			newestIdle = idle
		}
		for t := range sess.tags {
			tagCounts[t]++
		}
		return true
	})

	avgMessages := 0.0
	if sessionCount > 0 {
		avgMessages = float64(totalMessages) / float64(sessionCount)
	}

	resp := map[string]interface{}{
		"sessions": map[string]interface{}{
			"active":        sessionCount,
			"total_messages": totalMessages,
			"avg_messages":  fmt.Sprintf("%.1f", avgMessages),
		},
		"server": map[string]interface{}{
			"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
			"total_chats":    s.chatCount.Load(),
			"total_requests": requestCounter.Load(),
			"active_conns":   s.activeConns.Load(),
		},
		"tools": map[string]interface{}{
			"registered": len(s.registry.Names()),
		},
	}

	if sessionCount > 0 {
		resp["sessions"].(map[string]interface{})["oldest_idle_seconds"] = int(oldestIdle.Seconds())
		resp["sessions"].(map[string]interface{})["newest_idle_seconds"] = int(newestIdle.Seconds())
	}

	if len(tagCounts) > 0 {
		resp["tag_distribution"] = tagCounts
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleDeepHealth GET /v1/health/deep — 深度健康检查（含模型连通性）
func (s *HTTPServer) handleDeepHealth(w http.ResponseWriter, r *http.Request) {
	type checkResult struct {
		Name    string `json:"name"`
		Status  string `json:"status"` // "ok" or "error"
		Latency string `json:"latency,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	var checks []checkResult

	// 1. 基础健康
	checks = append(checks, checkResult{Name: "server", Status: "ok"})

	// 2. Memory store
	_, err := s.memStore.ReadMemory()
	if err != nil {
		checks = append(checks, checkResult{Name: "memory_store", Status: "error", Error: err.Error()})
	} else {
		checks = append(checks, checkResult{Name: "memory_store", Status: "ok"})
	}

	// 3. Session store
	if s.sessionStore != nil {
		_, err := s.sessionStore.LoadAll()
		if err != nil {
			checks = append(checks, checkResult{Name: "session_store", Status: "error", Error: err.Error()})
		} else {
			checks = append(checks, checkResult{Name: "session_store", Status: "ok"})
		}
	}

	// 4. Model connectivity (轻量 ping — 只需创建 agent 并检查配置)
	modelStatus := "ok"
	modelErr := ""
	if s.agentCfg.APIKey == "" {
		modelStatus = "warning"
		modelErr = "no API key configured"
	} else if s.agentCfg.BaseURL == "" && s.agentCfg.Provider == "" {
		modelStatus = "warning"
		modelErr = "no provider/base_url configured"
	}
	checks = append(checks, checkResult{
		Name:   "model_config",
		Status: modelStatus,
		Error:  modelErr,
	})

	// 5. (RAG removed)

	// 整体状态
	overall := "ok"
	for _, c := range checks {
		if c.Status == "error" {
			overall = "degraded"
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         overall,
		"checks":         checks,
		"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
	})
}

// -------- Tool Management --------

// handleDisableTool POST /v1/tools/{name}/disable — 运行时禁用工具
func (s *HTTPServer) handleDisableTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.registry.Get(name); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tool not found: " + name})
		return
	}
	s.disabledTools.Store(name, true)
	writeJSON(w, http.StatusOK, map[string]string{"message": "tool disabled", "tool": name})
}

// handleEnableTool POST /v1/tools/{name}/enable — 重新启用工具
func (s *HTTPServer) handleEnableTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.registry.Get(name); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tool not found: " + name})
		return
	}
	s.disabledTools.Delete(name)
	writeJSON(w, http.StatusOK, map[string]string{"message": "tool enabled", "tool": name})
}

// handleListDisabledTools GET /v1/tools/disabled — 列出禁用的工具
func (s *HTTPServer) handleListDisabledTools(w http.ResponseWriter, r *http.Request) {
	var disabled []string
	s.disabledTools.Range(func(key, _ any) bool {
		disabled = append(disabled, key.(string))
		return true
	})
	if disabled == nil {
		disabled = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"disabled": disabled,
		"count":    len(disabled),
	})
}

// -------- Plugin Management --------

// handleListPlugins GET /v1/plugins — 列出所有已加载的插件
func (s *HTTPServer) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	if s.pluginMgr == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": []string{}, "count": 0, "enabled": false})
		return
	}
	list := s.pluginMgr.List()
	type pluginInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Type        string `json:"type"`
		Timeout     int    `json:"timeout,omitempty"`
	}
	items := make([]pluginInfo, 0, len(list))
	for _, p := range list {
		items = append(items, pluginInfo{Name: p.Name, Description: p.Description, Type: string(p.Type), Timeout: p.Timeout})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": items, "count": len(items), "enabled": true})
}

// handleReloadPlugins POST /v1/plugins/reload — 重新加载插件目录
func (s *HTTPServer) handleReloadPlugins(w http.ResponseWriter, r *http.Request) {
	if s.pluginMgr == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plugin system not configured"})
		return
	}
	n, err := s.pluginMgr.LoadDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	registered := s.pluginMgr.RegisterAll(s.registry)
	writeJSON(w, http.StatusOK, map[string]interface{}{"loaded": n, "registered": registered})
}

// handleUnloadPlugin DELETE /v1/plugins/{name} — 卸载插件
func (s *HTTPServer) handleUnloadPlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.pluginMgr == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plugin system not configured"})
		return
	}
	if !s.pluginMgr.Unload(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin not found: " + name})
		return
	}
	s.registry.Unregister(name)
	writeJSON(w, http.StatusOK, map[string]string{"message": "plugin unloaded", "name": name})
}

// -------- Environment Info --------

// handleEnvInfo GET /v1/env — 返回运行环境信息
func (s *HTTPServer) handleEnvInfo(w http.ResponseWriter, r *http.Request) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"num_cpu":        runtime.NumCPU(),
		"num_goroutine":  runtime.NumGoroutine(),
		"alloc_mb":       float64(memStats.Alloc) / 1024 / 1024,
		"sys_mb":         float64(memStats.Sys) / 1024 / 1024,
		"gc_cycles":      memStats.NumGC,
		"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
		"provider":       s.agentCfg.Provider,
		"model":          s.agentCfg.Model,
	})
}

// -------- Session Rename --------

// handleRenameSession POST /v1/sessions/{session}/rename — 重命名会话
func (s *HTTPServer) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	oldID := r.PathValue("session")
	var req struct {
		NewID string `json:"new_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.NewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "new_id is required"})
		return
	}
	val, ok := s.sessions.Load(oldID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if _, exists := s.sessions.Load(req.NewID); exists {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "new_id already exists"})
		return
	}
	s.sessions.Store(req.NewID, val)
	s.sessions.Delete(oldID)

	// 持久化：删除旧快照
	if s.sessionStore != nil {
		_ = s.sessionStore.Delete(oldID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"old_id": oldID,
		"new_id": req.NewID,
	})
}

// -------- Tool Dry-Run --------

// handleToolDryRun POST /v1/tools/{name}/dry-run — 验证工具参数（不执行）
func (s *HTTPServer) handleToolDryRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tool, ok := s.registry.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tool not found: " + name})
		return
	}

	var args map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// 检查禁用状态
	_, disabled := s.disabledTools.Load(name)

	// 验证必需参数
	var missing []string
	for _, p := range tool.Parameters {
		if p.Required {
			if _, ok := args[p.Name]; !ok {
				missing = append(missing, p.Name)
			}
		}
	}

	type paramInfo struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Required    bool   `json:"required"`
		Description string `json:"description,omitempty"`
		Provided    bool   `json:"provided"`
	}
	params := make([]paramInfo, 0, len(tool.Parameters))
	for _, p := range tool.Parameters {
		_, provided := args[p.Name]
		params = append(params, paramInfo{
			Name: p.Name, Type: p.Type, Required: p.Required,
			Description: p.Description, Provided: provided,
		})
	}

	valid := len(missing) == 0
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tool":             name,
		"valid":            valid,
		"disabled":         disabled,
		"missing_required": missing,
		"parameters":       params,
		"timeout_ms":       tool.Timeout.Milliseconds(),
		"retryable":        tool.Retryable,
	})
}

// handleLockSession 锁定会话（禁止新消息）
func (s *HTTPServer) handleLockSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		LockedBy string `json:"locked_by"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.LockedBy == "" {
		body.LockedBy = "api"
	}

	if sess.locked {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":     "session already locked",
			"locked_by": sess.lockedBy,
		})
		return
	}

	sess.locked = true
	sess.lockedBy = body.LockedBy
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "locked",
		"session":   sid,
		"locked_by": sess.lockedBy,
	})
}

// handleUnlockSession 解锁会话
func (s *HTTPServer) handleUnlockSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	if !sess.locked {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "already_unlocked",
			"session": sid,
		})
		return
	}

	previousLocker := sess.lockedBy
	sess.locked = false
	sess.lockedBy = ""
	writeJSON(w, http.StatusOK, map[string]string{
		"status":          "unlocked",
		"session":         sid,
		"previous_locker": previousLocker,
	})
}

// handleSessionStats 返回会话统计信息
func (s *HTTPServer) handleSessionStats(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	hist := sess.agent.GetHistory()

	// 统计各角色消息数
	roleCounts := map[string]int{}
	totalChars := 0
	for _, msg := range hist {
		roleCounts[string(msg.Role)]++
		totalChars += len(msg.Content)
	}

	// 估算 token 数
	estimatedTokens := 0
	for _, ch := range func() string {
		var sb strings.Builder
		for _, m := range hist {
			sb.WriteString(m.Content)
		}
		return sb.String()
	}() {
		if ch > 0x4E00 && ch < 0x9FFF {
			estimatedTokens++
		} else {
			estimatedTokens++
		}
	}
	estimatedTokens = estimatedTokens * 10 / 40
	if estimatedTokens < 0 {
		estimatedTokens = 0
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":          sid,
		"message_count":    len(hist),
		"role_counts":      roleCounts,
		"total_chars":      totalChars,
		"estimated_tokens": estimatedTokens,
		"locked":           sess.locked,
		"locked_by":        sess.lockedBy,
		"has_custom_ttl":   sess.customTTL > 0,
	})
}

// handleBatchTools 批量执行多个工具
func (s *HTTPServer) handleBatchTools(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Tools   []struct {
			Name string                 `json:"name"`
			Args map[string]interface{} `json:"args"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(req.Tools) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tools array is required"})
		return
	}
	if len(req.Tools) > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 20 tools per batch"})
		return
	}

	type toolResult struct {
		Name    string      `json:"name"`
		Success bool        `json:"success"`
		Result  interface{} `json:"result,omitempty"`
		Error   string      `json:"error,omitempty"`
	}

	results := make([]toolResult, 0, len(req.Tools))
	for _, t := range req.Tools {
		start := time.Now()
		out, err := s.registry.Execute(r.Context(), t.Name, t.Args)
		elapsed := time.Since(start).Milliseconds()

		// 记录工具使用统计
		val, _ := s.toolUsage.LoadOrStore(t.Name, &toolUsageStats{})
		stats := val.(*toolUsageStats)
		stats.Calls.Add(1)
		stats.TotalMs.Add(elapsed)

		if err != nil {
			stats.Errors.Add(1)
			results = append(results, toolResult{Name: t.Name, Success: false, Error: err.Error()})
		} else {
			results = append(results, toolResult{Name: t.Name, Success: true, Result: out})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":   len(req.Tools),
		"results": results,
	})
}

// handleListTemplates 列出 prompt 模板
func (s *HTTPServer) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	var templates []promptTemplate
	s.promptTemplates.Range(func(key, val interface{}) bool {
		templates = append(templates, *val.(*promptTemplate))
		return true
	})
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"templates": templates,
		"total":     len(templates),
	})
}

// handleAddTemplate 添加 prompt 模板
func (s *HTTPServer) handleAddTemplate(w http.ResponseWriter, r *http.Request) {
	var tmpl promptTemplate
	if err := json.NewDecoder(r.Body).Decode(&tmpl); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if tmpl.Name == "" || tmpl.Template == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and template are required"})
		return
	}

	// 自动提取变量 {{var}}
	if len(tmpl.Variables) == 0 {
		seen := map[string]bool{}
		idx := 0
		for {
			start := strings.Index(tmpl.Template[idx:], "{{")
			if start == -1 {
				break
			}
			start += idx
			end := strings.Index(tmpl.Template[start:], "}}")
			if end == -1 {
				break
			}
			varName := strings.TrimSpace(tmpl.Template[start+2 : start+end])
			if varName != "" && !seen[varName] {
				tmpl.Variables = append(tmpl.Variables, varName)
				seen[varName] = true
			}
			idx = start + end + 2
		}
	}

	s.promptTemplates.Store(tmpl.Name, &tmpl)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "created",
		"template":  tmpl,
	})
}

// handleDeleteTemplate 删除 prompt 模板
func (s *HTTPServer) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.promptTemplates.LoadAndDelete(name); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "template not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
		"name":   name,
	})
}

// handleTrimMessages 裁剪会话历史（保留最近 N 条）
func (s *HTTPServer) handleTrimMessages(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		KeepLast int `json:"keep_last"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.KeepLast <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keep_last must be a positive integer"})
		return
	}

	hist := sess.agent.GetHistory()
	original := len(hist)
	if body.KeepLast >= len(hist) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session":  sid,
			"original": original,
			"trimmed":  0,
			"kept":     original,
		})
		return
	}

	trimmed := hist[len(hist)-body.KeepLast:]
	sess.agent.SetHistory(trimmed)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"original": original,
		"trimmed":  original - body.KeepLast,
		"kept":     body.KeepLast,
	})
}

// handleSetSystemPrompt 设置会话级 system prompt 覆盖
func (s *HTTPServer) handleSetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	sess.systemPromptOverride = body.Prompt
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"status":  "updated",
		"length":  len(body.Prompt),
	})
}

// handleGetSystemPrompt 获取会话级 system prompt
func (s *HTTPServer) handleGetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":    sid,
		"prompt":     sess.systemPromptOverride,
		"has_override": sess.systemPromptOverride != "",
	})
}

// handleImportSession 导入对话历史到会话
func (s *HTTPServer) handleImportSession(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Append bool `json:"append"` // true=追加，false=替换
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(body.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages array is required"})
		return
	}

	// 验证角色
	for i, m := range body.Messages {
		switch m.Role {
		case "user", "assistant", "system", "tool":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("invalid role at index %d: %s", i, m.Role),
			})
			return
		}
	}

	ag := s.getOrCreateSession(sid)
	imported := make([]*schema.Message, 0, len(body.Messages))
	for _, m := range body.Messages {
		imported = append(imported, &schema.Message{
			Role:    schema.RoleType(m.Role),
			Content: m.Content,
		})
	}

	if body.Append {
		existing := ag.GetHistory()
		combined := make([]*schema.Message, 0, len(existing)+len(imported))
		combined = append(combined, existing...)
		combined = append(combined, imported...)
		ag.SetHistory(combined)
	} else {
		ag.SetHistory(imported)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"imported": len(body.Messages),
		"mode":     map[bool]string{true: "append", false: "replace"}[body.Append],
		"total":    len(ag.GetHistory()),
	})
}

// handleInjectMessage 注入单条消息到会话历史
func (s *HTTPServer) handleInjectMessage(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Index   int    `json:"index"` // -1 或省略 = 追加到末尾
	}
	body.Index = -1
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}
	switch body.Role {
	case "user", "assistant", "system", "tool":
	case "":
		body.Role = "user"
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid role: " + body.Role})
		return
	}

	msg := &schema.Message{
		Role:    schema.RoleType(body.Role),
		Content: body.Content,
	}

	hist := sess.agent.GetHistory()
	if body.Index < 0 || body.Index >= len(hist) {
		// 追加到末尾
		hist = append(hist, msg)
	} else {
		// 在指定位置插入
		hist = append(hist[:body.Index+1], hist[body.Index:]...)
		hist[body.Index] = msg
	}
	sess.agent.SetHistory(hist)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"injected": true,
		"role":     body.Role,
		"index":    body.Index,
		"total":    len(hist),
	})
}

// -------- Event SSE Stream --------

// emitEvent 向所有 SSE 订阅者广播事件
func (s *HTTPServer) emitEvent(evType, session string, data interface{}) {
	evt := &serverEvent{
		Type:      evType,
		Session:   session,
		Data:      data,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	s.eventSubs.Range(func(_, val any) bool {
		ch := val.(chan *serverEvent)
		select {
		case ch <- evt:
		default: // 订阅者太慢，丢弃
		}
		return true
	})
}

// handleEventStream SSE 实时事件流
func (s *HTTPServer) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	subID := s.eventSubSeq.Add(1)
	ch := make(chan *serverEvent, 64)
	s.eventSubs.Store(subID, ch)
	defer func() {
		s.eventSubs.Delete(subID)
		close(ch)
	}()

	// 发送连接确认
	fmt.Fprintf(w, "event: connected\ndata: {\"subscriber_id\":%d}\n\n", subID)
	flusher.Flush()

	filter := r.URL.Query().Get("type") // 可选事件类型过滤

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if filter != "" && evt.Type != filter {
				continue
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// -------- Session Checkpoints --------

func (s *HTTPServer) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	// 检查同名检查点
	for _, cp := range sess.checkpoints {
		if cp.Name == body.Name {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "checkpoint already exists"})
			return
		}
	}

	hist := sess.agent.GetHistory()
	// 深拷贝历史
	copied := make([]*schema.Message, len(hist))
	for i, m := range hist {
		cp := *m
		copied[i] = &cp
	}

	sess.checkpoints = append(sess.checkpoints, sessionCheckpoint{
		Name:      body.Name,
		History:   copied,
		CreatedAt: time.Now().Format(time.RFC3339),
	})

	s.emitEvent("checkpoint.created", sid, map[string]string{"name": body.Name})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session":    sid,
		"checkpoint": body.Name,
		"messages":   len(copied),
		"total":      len(sess.checkpoints),
	})
}

func (s *HTTPServer) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	items := make([]map[string]interface{}, 0, len(sess.checkpoints))
	for _, cp := range sess.checkpoints {
		items = append(items, map[string]interface{}{
			"name":       cp.Name,
			"messages":   len(cp.History),
			"created_at": cp.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":     sid,
		"checkpoints": items,
	})
}

func (s *HTTPServer) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	for _, cp := range sess.checkpoints {
		if cp.Name == body.Name {
			// 深拷贝恢复
			restored := make([]*schema.Message, len(cp.History))
			for i, m := range cp.History {
				c := *m
				restored[i] = &c
			}
			sess.agent.SetHistory(restored)
			s.emitEvent("checkpoint.restored", sid, map[string]string{"name": body.Name})
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"session":    sid,
				"checkpoint": body.Name,
				"restored":   len(restored),
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
}

// -------- Message Edit / Delete / Undo --------

func (s *HTTPServer) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid index"})
		return
	}

	hist := sess.agent.GetHistory()
	if idx < 0 || idx >= len(hist) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "index out of range"})
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content is required"})
		return
	}

	oldContent := hist[idx].Content
	hist[idx].Content = body.Content
	sess.agent.SetHistory(hist)

	s.emitEvent("message.edited", sid, map[string]interface{}{
		"index":       idx,
		"old_length":  len(oldContent),
		"new_length":  len(body.Content),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"index":   idx,
		"role":    string(hist[idx].Role),
		"edited":  true,
	})
}

func (s *HTTPServer) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid index"})
		return
	}

	hist := sess.agent.GetHistory()
	if idx < 0 || idx >= len(hist) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "index out of range"})
		return
	}

	deleted := hist[idx]
	newHist := make([]*schema.Message, 0, len(hist)-1)
	newHist = append(newHist, hist[:idx]...)
	newHist = append(newHist, hist[idx+1:]...)
	sess.agent.SetHistory(newHist)

	s.emitEvent("message.deleted", sid, map[string]interface{}{"index": idx})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"deleted": map[string]interface{}{
			"index": idx,
			"role":  string(deleted.Role),
		},
		"remaining": len(newHist),
	})
}

func (s *HTTPServer) handleUndoMessage(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	hist := sess.agent.GetHistory()
	if len(hist) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no messages to undo"})
		return
	}

	removed := hist[len(hist)-1]
	sess.agent.SetHistory(hist[:len(hist)-1])

	s.emitEvent("message.undone", sid, map[string]interface{}{
		"role": string(removed.Role),
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"removed": map[string]interface{}{
			"role":    string(removed.Role),
			"length":  len(removed.Content),
		},
		"remaining": len(hist) - 1,
	})
}

// -------- Bulk Delete Sessions --------

func (s *HTTPServer) handleBulkDeleteSessions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.SessionIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_ids array is required"})
		return
	}
	if len(body.SessionIDs) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 100 sessions per request"})
		return
	}

	deleted := 0
	notFound := 0
	for _, id := range body.SessionIDs {
		if _, ok := s.sessions.LoadAndDelete(id); ok {
			if s.sessionStore != nil {
				_ = s.sessionStore.Delete(id)
			}
			deleted++
		} else {
			notFound++
		}
	}

	s.emitEvent("sessions.bulk_deleted", "", map[string]int{"deleted": deleted})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":   deleted,
		"not_found": notFound,
		"total":     len(body.SessionIDs),
	})
}

// -------- Tool Pipeline --------

func (s *HTTPServer) handleToolPipeline(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Steps []struct {
			Tool string                 `json:"tool"`
			Args map[string]interface{} `json:"args"`
		} `json:"steps"`
		StopOnError bool `json:"stop_on_error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(body.Steps) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "steps array is required"})
		return
	}
	if len(body.Steps) > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 10 steps per pipeline"})
		return
	}

	results := make([]map[string]interface{}, 0, len(body.Steps))
	var lastOutput string
	for i, step := range body.Steps {
		// 支持 {{prev}} 引用上一步输出
		args := make(map[string]interface{})
		for k, v := range step.Args {
			if str, ok := v.(string); ok && strings.Contains(str, "{{prev}}") {
				args[k] = strings.ReplaceAll(str, "{{prev}}", lastOutput)
			} else {
				args[k] = v
			}
		}

		start := time.Now()
		output, err := s.registry.Execute(r.Context(), step.Tool, args)
		elapsed := time.Since(start).Milliseconds()

		result := map[string]interface{}{
			"step":     i,
			"tool":     step.Tool,
			"elapsed":  elapsed,
		}
		if err != nil {
			result["error"] = err.Error()
			result["success"] = false
			results = append(results, result)
			if body.StopOnError {
				break
			}
			continue
		}

		lastOutput = output
		result["output"] = output
		result["success"] = true
		results = append(results, result)
	}

	s.emitEvent("pipeline.completed", "", map[string]int{"steps": len(results)})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

// -------- Session Manual Save --------

func (s *HTTPServer) handleSaveSession(w http.ResponseWriter, r *http.Request) {
	if s.sessionStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session persistence not configured"})
		return
	}
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	s.saveSession(sid, sess)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"saved":   true,
		"messages": len(sess.agent.GetHistory()),
	})
}

// -------- Session History Pagination --------

func (s *HTTPServer) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	hist := sess.agent.GetHistory()
	offset := 0
	limit := 50

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	// 角色过滤
	roleFilter := r.URL.Query().Get("role")

	var filtered []*schema.Message
	if roleFilter != "" {
		for _, m := range hist {
			if string(m.Role) == roleFilter {
				filtered = append(filtered, m)
			}
		}
	} else {
		filtered = hist
	}

	total := len(filtered)
	if offset >= total {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session":  sid,
			"messages": []*schema.Message{},
			"total":    total,
			"offset":   offset,
			"limit":    limit,
		})
		return
	}

	end := offset + limit
	if end > total {
		end = total
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"messages": filtered[offset:end],
		"total":    total,
		"offset":   offset,
		"limit":    limit,
		"has_more": end < total,
	})
}

// -------- Token Count --------

func (s *HTTPServer) handleTokenCount(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	hist := sess.agent.GetHistory()

	totalChars := 0
	perRole := make(map[string]int)
	for _, m := range hist {
		totalChars += len(m.Content)
		perRole[string(m.Role)] += len(m.Content)
	}

	// 估算 token 数（CJK 约 1 字/token，英文约 4 字符/token）
	estimateTokens := func(chars int) int {
		return chars/2 + 1 // 保守估计
	}

	roleTokens := make(map[string]int)
	for role, chars := range perRole {
		roleTokens[role] = estimateTokens(chars)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":         sid,
		"total_chars":     totalChars,
		"estimated_tokens": estimateTokens(totalChars),
		"per_role_tokens": roleTokens,
		"messages":        len(hist),
		"context_limit":   s.contextLength,
		"usage_percent":   float64(estimateTokens(totalChars)) / float64(s.contextLength) * 100,
	})
}

// -------- Session Metadata --------

func (s *HTTPServer) handleGetSessionMeta(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	meta := sess.metadata
	if meta == nil {
		meta = map[string]string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"metadata": meta,
	})
}

func (s *HTTPServer) handleSetSessionMeta(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON object"})
		return
	}

	if sess.metadata == nil {
		sess.metadata = make(map[string]string)
	}
	for k, v := range body {
		if v == "" {
			delete(sess.metadata, k)
		} else {
			sess.metadata[k] = v
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"metadata": sess.metadata,
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// clientIP 提取客户端真实 IP（支持 X-Forwarded-For）
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if parts := strings.SplitN(fwd, ",", 2); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if host, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return host
	}
	return r.RemoteAddr
}

// ──────── Batch Export ────────

func (s *HTTPServer) handleBatchExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sessions []string `json:"sessions"`
		Format   string   `json:"format"` // "json" (default) or "markdown"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(req.Sessions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessions list required"})
		return
	}
	if len(req.Sessions) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max 50 sessions per export"})
		return
	}

	type exportedSession struct {
		ID       string                   `json:"id"`
		Title    string                   `json:"title,omitempty"`
		Messages []map[string]interface{} `json:"messages"`
		Tags     []string                 `json:"tags,omitempty"`
		Starred  bool                     `json:"starred"`
		Archived bool                     `json:"archived"`
	}
	var results []exportedSession
	var notFound []string

	for _, sid := range req.Sessions {
		val, ok := s.sessions.Load(sid)
		if !ok {
			notFound = append(notFound, sid)
			continue
		}
		sess := val.(*httpSession)
		hist := sess.agent.GetHistory()
		var msgs []map[string]interface{}
		for _, m := range hist {
			msgs = append(msgs, map[string]interface{}{
				"role":    string(m.Role),
				"content": m.Content,
			})
		}
		if msgs == nil {
			msgs = []map[string]interface{}{}
		}
		var tagList []string
		for t := range sess.tags {
			tagList = append(tagList, t)
		}
		results = append(results, exportedSession{
			ID:       sid,
			Title:    sess.title,
			Messages: msgs,
			Tags:     tagList,
			Starred:  sess.starred,
			Archived: sess.archived,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"exported":  results,
		"count":     len(results),
		"not_found": notFound,
	})
}

// ──────── Global Message Search ────────

func (s *HTTPServer) handleGlobalSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' required"})
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	qLower := strings.ToLower(q)

	type searchResult struct {
		Session string `json:"session"`
		Index   int    `json:"index"`
		Role    string `json:"role"`
		Preview string `json:"preview"`
	}
	var results []searchResult

	s.sessions.Range(func(key, val any) bool {
		sid := key.(string)
		sess := val.(*httpSession)
		hist := sess.agent.GetHistory()
		for i, m := range hist {
			if strings.Contains(strings.ToLower(m.Content), qLower) {
				preview := m.Content
				if len(preview) > 150 {
					preview = preview[:150] + "..."
				}
				results = append(results, searchResult{
					Session: sid,
					Index:   i,
					Role:    string(m.Role),
					Preview: preview,
				})
				if len(results) >= limit {
					return false
				}
			}
		}
		return len(results) < limit
	})
	if results == nil {
		results = []searchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results, "count": len(results), "query": q})
}

// ──────── Session Sharing ────────

func generateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ──────── Batch 5: Tool Usage / System Info / Session Rename / YAML Export / etc. ────────

func (s *HTTPServer) handleToolUsage(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	tracker := sess.agent.GetTracker()

	summary := tracker.Summary()
	turns := tracker.GetTurns()

	// 按工具维度统计
	type toolDetail struct {
		Name       string  `json:"name"`
		CallCount  int     `json:"call_count"`
		ErrorCount int     `json:"error_count"`
		ErrorRate  float64 `json:"error_rate"`
		AvgDurMs   float64 `json:"avg_duration_ms"`
	}

	toolStats := map[string]*struct {
		calls, errors int
		totalDur      time.Duration
	}{}
	for _, turn := range turns {
		for _, tc := range turn.ToolCalls {
			st, exists := toolStats[tc.ToolName]
			if !exists {
				st = &struct {
					calls, errors int
					totalDur      time.Duration
				}{}
				toolStats[tc.ToolName] = st
			}
			st.calls++
			if !tc.Success {
				st.errors++
			}
			st.totalDur += tc.Duration
		}
	}

	details := make([]toolDetail, 0, len(toolStats))
	for name, st := range toolStats {
		errRate := 0.0
		if st.calls > 0 {
			errRate = float64(st.errors) / float64(st.calls) * 100
		}
		avgDur := 0.0
		if st.calls > 0 {
			avgDur = float64(st.totalDur.Milliseconds()) / float64(st.calls)
		}
		details = append(details, toolDetail{
			Name:       name,
			CallCount:  st.calls,
			ErrorCount: st.errors,
			ErrorRate:  errRate,
			AvgDurMs:   avgDur,
		})
	}
	sort.Slice(details, func(i, j int) bool { return details[i].CallCount > details[j].CallCount })

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":       sid,
		"summary":       summary,
		"tool_details":  details,
		"unique_tools":  len(details),
	})
}

func (s *HTTPServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	toolNames := []string{}
	if s.registry != nil {
		toolNames = s.registry.Names()
	}

	hasFallback := false
	if s.fallbackCfg != nil {
		hasFallback = true
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":  "0.2.0",
		"model":    s.agentCfg.Model,
		"provider": s.agentCfg.Provider,
		"features": map[string]bool{
			"streaming":           true,
			"sse_events":          true,
			"websocket":           true,
			"openai_compatible":   true,
			"fallback":            hasFallback,
			"turn_tracking":       true,
			"session_persistence": true,
			"rate_limiting":       s.rateLimiter != nil,
			"templates":           true,
			"checkpoints":         true,
			"branching":           true,
		},
		"tools":       toolNames,
		"tool_count":  len(toolNames),
		"api_version": "v1",
	})
}

func (s *HTTPServer) handleContextWindow(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	history := sess.agent.GetHistory()
	totalChars := 0
	totalTokensEst := 0
	for _, msg := range history {
		totalChars += len(msg.Content)
	}
	// 粗略估算：英文 1 token ≈ 4 chars，中文 1 token ≈ 2 chars
	totalTokensEst = totalChars / 3

	ctxLen := s.contextLength
	if ctxLen <= 0 {
		ctxLen = 128000
	}

	usage := 0.0
	if ctxLen > 0 {
		usage = float64(totalTokensEst) / float64(ctxLen) * 100
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":             sid,
		"context_length":      ctxLen,
		"estimated_tokens":    totalTokensEst,
		"total_chars":         totalChars,
		"message_count":       len(history),
		"usage_percent":       usage,
		"remaining_tokens":    ctxLen - totalTokensEst,
		"compression_needed":  usage > 80,
	})
}

// handleSessionFromTemplate POST /v1/sessions/{session}/from-template — 从模板初始化会话
func (s *HTTPServer) handleSessionFromTemplate(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	var req struct {
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Template == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "template name required"})
		return
	}
	val, ok := s.sessionTemplates.Load(req.Template)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "template not found"})
		return
	}
	tmpl := val.(*sessionTemplate)
	ag := s.getOrCreateSession(sid)
	if tmpl.SystemPrompt != "" {
		ag.SetExtraSystemPrompt(tmpl.SystemPrompt)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":  sid,
		"template": req.Template,
		"applied":  true,
	})
}

// ──── Batch 8 Handlers: ADK Checkpoint/Resume (Eino v0.8.4) ────

// handleListADKCheckpoints GET /v1/adk/checkpoints — 列出所有 ADK 检查点
func (s *HTTPServer) handleListADKCheckpoints(w http.ResponseWriter, r *http.Request) {
	if s.adkCheckpointStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ADK checkpoint store not initialized"})
		return
	}
	metas, err := s.adkCheckpointStore.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if metas == nil {
		metas = []agent.CheckpointMetaInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"checkpoints": metas,
		"count":       len(metas),
	})
}

// handleSaveADKCheckpoint POST /v1/adk/checkpoints — 手动保存 ADK 检查点
func (s *HTTPServer) handleSaveADKCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.adkCheckpointStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ADK checkpoint store not initialized"})
		return
	}
	var req struct {
		Key  string `json:"key"`
		Data string `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key is required"})
		return
	}
	if err := s.adkCheckpointStore.Set(r.Context(), req.Key, []byte(req.Data)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"key":    req.Key,
		"status": "saved",
	})
}

// handleGetADKCheckpoint GET /v1/adk/checkpoints/{key} — 获取指定 ADK 检查点
func (s *HTTPServer) handleGetADKCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.adkCheckpointStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ADK checkpoint store not initialized"})
		return
	}
	key := r.PathValue("key")
	data, ok, err := s.adkCheckpointStore.Get(r.Context(), key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "checkpoint not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"key":  key,
		"size": len(data),
		"data": string(data),
	})
}

// handleDeleteADKCheckpoint DELETE /v1/adk/checkpoints/{key} — 删除 ADK 检查点
func (s *HTTPServer) handleDeleteADKCheckpoint(w http.ResponseWriter, r *http.Request) {
	if s.adkCheckpointStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ADK checkpoint store not initialized"})
		return
	}
	key := r.PathValue("key")
	if err := s.adkCheckpointStore.Delete(key); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"key":    key,
		"status": "deleted",
	})
}
