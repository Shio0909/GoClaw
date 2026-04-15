package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"github.com/goclaw/goclaw/audit"
	"github.com/goclaw/goclaw/config"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/rag"
	"github.com/goclaw/goclaw/tools"
	"github.com/goclaw/goclaw/webhook"
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
	ragMgr         *rag.Manager
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
	auditLog       *audit.Log   // 审计日志
	webhookMgr     *webhook.Manager // Webhook 管理器
	disabledTools  sync.Map         // toolName -> bool，运行时禁用的工具
	endpointStats  sync.Map         // endpoint -> *endpointStat，端点延迟统计
	pluginMgr      *tools.PluginManager // 插件管理器（可选）
	cronJobs       sync.Map             // jobID -> *cronJob
	toolAliases    sync.Map             // alias -> realName
	toolUsage      sync.Map             // toolName -> *toolUsageStats
	promptTemplates sync.Map            // name -> *promptTemplate

	server *http.Server
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

type httpSession struct {
	agent       *agent.Agent
	memMgr      *memory.Manager
	lastUsed    time.Time
	tags        map[string]bool // 会话标签
	annotations []sessionNote   // 会话备注
	customTTL   time.Duration   // 自定义存活时间（0 = 使用全局默认）
	locked      bool            // 锁定状态（锁定后禁止新消息）
	lockedBy    string          // 锁定者标识
	systemPromptOverride string // 会话级 system prompt 覆盖
}

type sessionNote struct {
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
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
	RAGManager     *rag.Manager
	ContextLength  int
	APIToken       string   // 可选，设置后需 Bearer Token 认证
	CORSOrigins    []string // CORS 允许的域名，["*"] 为全部
	SessionTimeout int      // 会话超时（分钟），默认 30
	RequestTimeout int      // 请求超时（秒），默认 300
	SessionDir     string   // 会话持久化目录，空则不持久化
	RateLimit      int      // 每分钟请求限制（0 = 不限制）
	FallbackCfg    *agent.FallbackConfig // 模型回退配置（可选）
	ConfigPath     string                // 配置文件路径（用于热重载）
	AuditLog       *audit.Log            // 审计日志（可选）
	WebhookMgr     *webhook.Manager      // Webhook 管理器（可选）
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
		ragMgr:         cfg.RAGManager,
		contextLength:  ctxLen,
		apiToken:       cfg.APIToken,
		corsOrigins:    cfg.CORSOrigins,
		sessionTimeout: sessTimeout,
		requestTimeout: reqTimeout,
		sessionStore:   store,
		shutdownCh:     make(chan struct{}),
		configPath:     cfg.ConfigPath,
		auditLog:       cfg.AuditLog,
		webhookMgr:     cfg.WebhookMgr,
	}

	// 从磁盘恢复会话
	if store != nil {
		srv.restoreSessions()
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
	mux.HandleFunc("GET /v1/tools/stats", s.handleToolStats)
	mux.HandleFunc("GET /v1/memory/{session}", s.handleGetMemory)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/sessions", s.handleListSessions)
	mux.HandleFunc("POST /v1/sessions/{session}/fork", s.handleForkSession)
	mux.HandleFunc("GET /v1/config", s.handleGetConfig)
	mux.HandleFunc("GET /v1/openapi.json", s.handleOpenAPISpec)
	mux.HandleFunc("POST /v1/config/reload", s.handleConfigReload)
	mux.HandleFunc("GET /v1/sessions/search", s.handleSessionSearch)
	mux.HandleFunc("GET /v1/audit", s.handleAuditQuery)
	mux.HandleFunc("GET /v1/webhooks", s.handleListWebhooks)
	mux.HandleFunc("POST /v1/webhooks", s.handleAddWebhook)
	mux.HandleFunc("DELETE /v1/webhooks", s.handleRemoveWebhook)
	mux.HandleFunc("GET /v1/rate-limit", s.handleRateLimitStatus)
	mux.HandleFunc("PUT /v1/sessions/{session}/tags", s.handleSetTags)
	mux.HandleFunc("GET /v1/sessions/{session}/tags", s.handleGetTags)
	mux.HandleFunc("DELETE /v1/sessions/{session}/tags", s.handleDeleteTag)
	mux.HandleFunc("POST /v1/sessions/{session}/annotate", s.handleAnnotateSession)
	mux.HandleFunc("GET /v1/sessions/{session}/annotations", s.handleGetAnnotations)
	mux.HandleFunc("POST /v1/batch/chat", s.handleBatchChat)
	mux.HandleFunc("POST /v1/admin/gc", s.handleAdminGC)
	mux.HandleFunc("GET /v1/analytics", s.handleAnalytics)
	mux.HandleFunc("GET /v1/health/deep", s.handleDeepHealth)
	mux.HandleFunc("POST /v1/tools/{name}/disable", s.handleDisableTool)
	mux.HandleFunc("POST /v1/tools/{name}/enable", s.handleEnableTool)
	mux.HandleFunc("GET /v1/tools/disabled", s.handleListDisabledTools)
	mux.HandleFunc("GET /v1/latency", s.handleLatencyStats)
	// Plugin management
	mux.HandleFunc("GET /v1/plugins", s.handleListPlugins)
	mux.HandleFunc("POST /v1/plugins/reload", s.handleReloadPlugins)
	mux.HandleFunc("DELETE /v1/plugins/{name}", s.handleUnloadPlugin)
	// Cron / scheduled tasks
	mux.HandleFunc("GET /v1/cron", s.handleListCronJobs)
	mux.HandleFunc("POST /v1/cron", s.handleAddCronJob)
	mux.HandleFunc("DELETE /v1/cron/{id}", s.handleDeleteCronJob)
	// Session TTL
	mux.HandleFunc("PUT /v1/sessions/{session}/ttl", s.handleSetSessionTTL)
	// Tool aliases
	mux.HandleFunc("GET /v1/tools/aliases", s.handleListToolAliases)
	mux.HandleFunc("PUT /v1/tools/aliases", s.handleSetToolAlias)
	mux.HandleFunc("DELETE /v1/tools/aliases/{alias}", s.handleDeleteToolAlias)
	// Debug
	mux.HandleFunc("GET /v1/debug/routes", s.handleDebugRoutes)
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
	mux.HandleFunc("POST /v1/estimate-cost", s.handleEstimateCost)
	// Session stats
	mux.HandleFunc("GET /v1/sessions/{session}/stats", s.handleSessionStats)
	// Batch tool execution
	mux.HandleFunc("POST /v1/tools/batch", s.handleBatchTools)
	// Tool analytics
	mux.HandleFunc("GET /v1/tools/analytics", s.handleToolAnalytics)
	mux.HandleFunc("DELETE /v1/tools/analytics", s.handleResetToolAnalytics)
	// Prompt templates
	mux.HandleFunc("GET /v1/templates", s.handleListTemplates)
	mux.HandleFunc("POST /v1/templates", s.handleAddTemplate)
	mux.HandleFunc("DELETE /v1/templates/{name}", s.handleDeleteTemplate)
	// Session message search
	mux.HandleFunc("GET /v1/sessions/{session}/search", s.handleSearchMessages)
	// Session message trim
	mux.HandleFunc("POST /v1/sessions/{session}/trim", s.handleTrimMessages)
	// System prompt override
	mux.HandleFunc("PUT /v1/sessions/{session}/system-prompt", s.handleSetSystemPrompt)
	mux.HandleFunc("GET /v1/sessions/{session}/system-prompt", s.handleGetSystemPrompt)
	// Session comparison
	mux.HandleFunc("POST /v1/sessions/compare", s.handleCompareSessions)
	// Conversation summary
	mux.HandleFunc("GET /v1/sessions/{session}/summary", s.handleSessionSummary)
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
		if sess, ok := val.(*httpSession); ok && sess.locked {
			writeJSON(w, http.StatusLocked, map[string]string{
				"error":     "session is locked",
				"locked_by": sess.lockedBy,
			})
			return
		}
	}

	s.chatCount.Add(1)

	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventChatStart, req.Session, "", clientIP(r), nil)
	}

	if req.Stream {
		s.handleStreamChat(w, r, ag, req)
		return
	}

	// 非流式
	resp, err := ag.Run(r.Context(), req.Message)
	if err != nil {
		if s.auditLog != nil {
			s.auditLog.Emit(audit.EventError, req.Session, err.Error(), clientIP(r), nil)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventChatEnd, req.Session, "", clientIP(r), nil)
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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventSessionDelete, sessionID, "", clientIP(r), nil)
	}
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

func (s *HTTPServer) handleToolStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, tools.GetGlobalToolStats().Summary())
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
	if s.ragMgr != nil {
		resp["rag_enabled"] = true
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

	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventSessionFork, sourceID, req.NewSession, clientIP(r), nil)
	}

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
		"rag":         s.ragMgr != nil,
		"persistence": s.sessionStore != nil,
		"audit_log":   s.auditLog != nil,
		"webhooks":    s.webhookMgr != nil,
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

	// 审计日志
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", fmt.Sprintf("%d changes", len(changes)), clientIP(r), nil)
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

// handleAuditQuery GET /v1/audit?type=chat_end&limit=50&since_id=0
func (s *HTTPServer) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	if s.auditLog == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
			"events":  []interface{}{},
		})
		return
	}

	typ := audit.EventType(r.URL.Query().Get("type"))
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	var sinceID int64
	if s := r.URL.Query().Get("since_id"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			sinceID = v
		}
	}

	events := s.auditLog.Query(typ, limit, sinceID)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"total":   s.auditLog.Count(),
		"count":   len(events),
		"events":  events,
	})
}

// handleListWebhooks GET /v1/webhooks — 列出所有 webhook 及发送统计
func (s *HTTPServer) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if s.webhookMgr == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
			"hooks":   []interface{}{},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled": true,
		"hooks":   s.webhookMgr.ListHooks(),
		"stats":   s.webhookMgr.Stats(),
	})
}

// handleAddWebhook POST /v1/webhooks — 动态添加 webhook
func (s *HTTPServer) handleAddWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookMgr == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "webhooks not enabled"})
		return
	}

	var hook webhook.Hook
	if err := json.NewDecoder(r.Body).Decode(&hook); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if hook.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	s.webhookMgr.AddHook(hook)

	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", "webhook added: "+hook.URL, clientIP(r), nil)
	}

	writeJSON(w, http.StatusCreated, map[string]string{"message": "webhook added", "url": hook.URL})
}

// handleRemoveWebhook DELETE /v1/webhooks — 按 URL 移除 webhook
func (s *HTTPServer) handleRemoveWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookMgr == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "webhooks not enabled"})
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	if s.webhookMgr.RemoveHook(req.URL) {
		writeJSON(w, http.StatusOK, map[string]string{"message": "webhook removed", "url": req.URL})
	} else {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook not found"})
	}
}

// handleRateLimitStatus GET /v1/rate-limit — 速率限制状态
func (s *HTTPServer) handleRateLimitStatus(w http.ResponseWriter, r *http.Request) {
	if s.rateLimiter == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"enabled": false,
		})
		return
	}

	status := s.rateLimiter.Status()
	status["enabled"] = true
	writeJSON(w, http.StatusOK, status)
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
	if s.ragMgr != nil {
		ag.SetRAGManager(s.ragMgr)
	}
	if s.fallbackCfg != nil {
		ag.SetFallbackConfig(s.fallbackCfg)
	}

	s.sessions.Store(id, &httpSession{
		agent:    ag,
		memMgr:   memMgr,
		lastUsed: time.Now(),
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

// handleDeleteTag DELETE /v1/sessions/{session}/tags — 移除标签
func (s *HTTPServer) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tag == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tag is required"})
		return
	}

	delete(sess.tags, req.Tag)

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

// handleAnnotateSession POST /v1/sessions/{session}/annotate — 添加会话备注
func (s *HTTPServer) handleAnnotateSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	note := sessionNote{
		Text:      strings.TrimSpace(req.Text),
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	sess.annotations = append(sess.annotations, note)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sessionID,
		"count":   len(sess.annotations),
		"note":    note,
	})
}

// handleGetAnnotations GET /v1/sessions/{session}/annotations — 获取备注列表
func (s *HTTPServer) handleGetAnnotations(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)

	notes := sess.annotations
	if notes == nil {
		notes = []sessionNote{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":     sessionID,
		"count":       len(notes),
		"annotations": notes,
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

	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventAdminGC, "", fmt.Sprintf("gc: cleaned %d sessions (threshold=%v)", len(cleaned), threshold), clientIP(r), nil)
	}

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

	if s.auditLog != nil {
		resp["audit"] = s.auditLog.Counts()
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

	// 5. RAG
	if s.ragMgr != nil {
		checks = append(checks, checkResult{Name: "rag", Status: "ok"})
	}

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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", "tool disabled: "+name, clientIP(r), nil)
	}
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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", "tool enabled: "+name, clientIP(r), nil)
	}
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

// -------- Latency Stats --------

// handleLatencyStats GET /v1/latency — 端点延迟统计
func (s *HTTPServer) handleLatencyStats(w http.ResponseWriter, r *http.Request) {
	type latencyEntry struct {
		Endpoint string  `json:"endpoint"`
		Calls    int64   `json:"calls"`
		Errors   int64   `json:"errors"`
		AvgMs    float64 `json:"avg_ms"`
	}

	var entries []latencyEntry
	s.endpointStats.Range(func(key, val any) bool {
		stat := val.(*endpointStat)
		calls := stat.calls.Load()
		avgMs := 0.0
		if calls > 0 {
			avgMs = float64(stat.totalMs.Load()) / float64(calls)
		}
		entries = append(entries, latencyEntry{
			Endpoint: key.(string),
			Calls:    calls,
			Errors:   stat.errors.Load(),
			AvgMs:    avgMs,
		})
		return true
	})

	if entries == nil {
		entries = []latencyEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"endpoints": entries,
		"count":     len(entries),
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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", fmt.Sprintf("plugins reloaded: %d loaded, %d registered", n, registered), clientIP(r), nil)
	}
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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, "", "plugin unloaded: "+name, clientIP(r), nil)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "plugin unloaded", "name": name})
}

// -------- Cron / Scheduled Tasks --------

type cronJob struct {
	ID       string `json:"id"`
	Session  string `json:"session"`
	Message  string `json:"message"`
	Interval int    `json:"interval_seconds"` // 执行间隔（秒）
	NextRun  string `json:"next_run"`
	RunCount int64  `json:"run_count"`

	ticker  *time.Ticker
	stopCh  chan struct{}
	runCnt  atomic.Int64
	nextRun atomic.Value // time.Time
}

func (s *HTTPServer) handleListCronJobs(w http.ResponseWriter, r *http.Request) {
	var jobs []map[string]interface{}
	s.cronJobs.Range(func(key, val any) bool {
		cj := val.(*cronJob)
		nr, _ := cj.nextRun.Load().(time.Time)
		jobs = append(jobs, map[string]interface{}{
			"id":               cj.ID,
			"session":          cj.Session,
			"message":          cj.Message,
			"interval_seconds": cj.Interval,
			"next_run":         nr.Format(time.RFC3339),
			"run_count":        cj.runCnt.Load(),
		})
		return true
	})
	if jobs == nil {
		jobs = []map[string]interface{}{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": jobs, "count": len(jobs)})
}

func (s *HTTPServer) handleAddCronJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session  string `json:"session"`
		Message  string `json:"message"`
		Interval int    `json:"interval_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Session == "" || req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session and message required"})
		return
	}
	if req.Interval < 10 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "interval must be >= 10 seconds"})
		return
	}
	if req.Interval > 86400 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "interval must be <= 86400 seconds (1 day)"})
		return
	}

	id := fmt.Sprintf("cron_%d", time.Now().UnixNano())
	cj := &cronJob{
		ID:       id,
		Session:  req.Session,
		Message:  req.Message,
		Interval: req.Interval,
		ticker:   time.NewTicker(time.Duration(req.Interval) * time.Second),
		stopCh:   make(chan struct{}),
	}
	cj.nextRun.Store(time.Now().Add(time.Duration(req.Interval) * time.Second))
	s.cronJobs.Store(id, cj)

	go s.runCronJob(cj)

	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, req.Session, fmt.Sprintf("cron added: %s every %ds", id, req.Interval), clientIP(r), nil)
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id": id, "session": req.Session, "interval_seconds": req.Interval,
	})
}

func (s *HTTPServer) handleDeleteCronJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	val, ok := s.cronJobs.LoadAndDelete(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cron job not found"})
		return
	}
	cj := val.(*cronJob)
	close(cj.stopCh)
	cj.ticker.Stop()
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, cj.Session, "cron deleted: "+id, clientIP(r), nil)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "cron job deleted", "id": id})
}

func (s *HTTPServer) runCronJob(cj *cronJob) {
	for {
		select {
		case <-cj.ticker.C:
			cj.nextRun.Store(time.Now().Add(time.Duration(cj.Interval) * time.Second))
			ag := s.getOrCreateSession(cj.Session)
			ctx, cancel := context.WithTimeout(context.Background(), s.requestTimeout)
			_, err := ag.Run(ctx, cj.Message)
			cancel()
			cj.runCnt.Add(1)
			if err != nil {
				log.Printf("[Cron] %s 执行失败: %v", cj.ID, err)
			}
		case <-cj.stopCh:
			return
		}
	}
}

// -------- Session TTL --------

// handleSetSessionTTL PUT /v1/sessions/{session}/ttl — 设置会话自定义存活时间
func (s *HTTPServer) handleSetSessionTTL(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session")
	val, ok := s.sessions.Load(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	var req struct {
		TTLMinutes int `json:"ttl_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.TTLMinutes < 1 || req.TTLMinutes > 10080 { // 1 分钟 ~ 7 天
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ttl_minutes must be 1-10080 (7 days)"})
		return
	}
	sess := val.(*httpSession)
	sess.customTTL = time.Duration(req.TTLMinutes) * time.Minute
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":     sessionID,
		"ttl_minutes": req.TTLMinutes,
	})
}

// -------- Tool Aliases --------

// handleListToolAliases GET /v1/tools/aliases — 列出工具别名
func (s *HTTPServer) handleListToolAliases(w http.ResponseWriter, r *http.Request) {
	aliases := make(map[string]string)
	s.toolAliases.Range(func(key, val any) bool {
		aliases[key.(string)] = val.(string)
		return true
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"aliases": aliases, "count": len(aliases)})
}

// handleSetToolAlias PUT /v1/tools/aliases — 设置工具别名
func (s *HTTPServer) handleSetToolAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Alias string `json:"alias"`
		Tool  string `json:"tool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Alias == "" || req.Tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alias and tool are required"})
		return
	}
	if _, ok := s.registry.Get(req.Tool); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tool not found: " + req.Tool})
		return
	}
	s.toolAliases.Store(req.Alias, req.Tool)
	writeJSON(w, http.StatusOK, map[string]string{"alias": req.Alias, "tool": req.Tool})
}

// handleDeleteToolAlias DELETE /v1/tools/aliases/{alias} — 删除工具别名
func (s *HTTPServer) handleDeleteToolAlias(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("alias")
	if _, ok := s.toolAliases.LoadAndDelete(alias); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "alias not found: " + alias})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "alias deleted", "alias": alias})
}

// -------- Debug Routes --------

// handleDebugRoutes GET /v1/debug/routes — 列出所有注册的路由
func (s *HTTPServer) handleDebugRoutes(w http.ResponseWriter, r *http.Request) {
	routes := []string{
		"POST /v1/chat", "GET /v1/chat/{session}", "DELETE /v1/chat/{session}",
		"GET /v1/chat/{session}/export", "GET /v1/tools", "GET /v1/tools/stats",
		"GET /v1/memory/{session}", "GET /v1/health", "GET /v1/metrics",
		"GET /v1/sessions", "POST /v1/sessions/{session}/fork", "GET /v1/config",
		"GET /v1/openapi.json", "POST /v1/config/reload", "GET /v1/sessions/search",
		"GET /v1/audit", "GET /v1/webhooks", "POST /v1/webhooks", "DELETE /v1/webhooks",
		"GET /v1/rate-limit",
		"PUT /v1/sessions/{session}/tags", "GET /v1/sessions/{session}/tags",
		"DELETE /v1/sessions/{session}/tags",
		"POST /v1/sessions/{session}/annotate", "GET /v1/sessions/{session}/annotations",
		"POST /v1/batch/chat", "POST /v1/admin/gc", "GET /v1/analytics",
		"GET /v1/health/deep",
		"POST /v1/tools/{name}/disable", "POST /v1/tools/{name}/enable",
		"GET /v1/tools/disabled", "GET /v1/latency",
		"GET /v1/plugins", "POST /v1/plugins/reload", "DELETE /v1/plugins/{name}",
		"GET /v1/cron", "POST /v1/cron", "DELETE /v1/cron/{id}",
		"PUT /v1/sessions/{session}/ttl",
		"GET /v1/tools/aliases", "PUT /v1/tools/aliases", "DELETE /v1/tools/aliases/{alias}",
		"GET /v1/debug/routes",
		"POST /v1/chat/completions", "GET /v1/models",
		"GET /v1/ws",
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"routes": routes,
		"count":  len(routes),
	})
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
	if s.auditLog != nil {
		s.auditLog.Emit(audit.EventConfigReload, oldID, "session renamed to: "+req.NewID, clientIP(r), nil)
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

// handleEstimateCost 估算消息的 token 成本
func (s *HTTPServer) handleEstimateCost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message       string  `json:"message"`
		Model         string  `json:"model"`
		InputPricePer1K  float64 `json:"input_price_per_1k"`
		OutputPricePer1K float64 `json:"output_price_per_1k"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	// 简单 token 估算：英文约 4 字符/token，CJK 约 2 字符/token
	inputTokens := 0
	for _, ch := range req.Message {
		if ch > 0x4E00 && ch < 0x9FFF {
			inputTokens += 1 // CJK 字符约 1 token
		} else {
			inputTokens += 1
		}
	}
	inputTokens = inputTokens * 10 / 40 // 粗略除以 4
	if inputTokens < 1 {
		inputTokens = 1
	}

	// 估算输出（假设 2:1 输出/输入比）
	estimatedOutputTokens := inputTokens * 2

	// 价格计算
	if req.InputPricePer1K == 0 {
		req.InputPricePer1K = 0.003 // 默认价格
	}
	if req.OutputPricePer1K == 0 {
		req.OutputPricePer1K = 0.006
	}

	inputCost := float64(inputTokens) / 1000.0 * req.InputPricePer1K
	outputCost := float64(estimatedOutputTokens) / 1000.0 * req.OutputPricePer1K

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"input_tokens":           inputTokens,
		"estimated_output_tokens": estimatedOutputTokens,
		"input_cost":             inputCost,
		"output_cost":            outputCost,
		"total_estimated_cost":   inputCost + outputCost,
		"model":                  req.Model,
		"currency":               "USD",
		"note":                   "rough estimate based on character count heuristics",
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

// handleToolAnalytics 返回工具使用统计
func (s *HTTPServer) handleToolAnalytics(w http.ResponseWriter, r *http.Request) {
	type stat struct {
		Name      string  `json:"name"`
		Calls     int64   `json:"calls"`
		Errors    int64   `json:"errors"`
		AvgMs     float64 `json:"avg_ms"`
		TotalMs   int64   `json:"total_ms"`
		ErrorRate float64 `json:"error_rate"`
	}

	var stats []stat
	s.toolUsage.Range(func(key, val interface{}) bool {
		name := key.(string)
		u := val.(*toolUsageStats)
		calls := u.Calls.Load()
		errs := u.Errors.Load()
		totalMs := u.TotalMs.Load()
		var avgMs float64
		if calls > 0 {
			avgMs = float64(totalMs) / float64(calls)
		}
		var errRate float64
		if calls > 0 {
			errRate = float64(errs) / float64(calls)
		}
		stats = append(stats, stat{
			Name: name, Calls: calls, Errors: errs,
			AvgMs: avgMs, TotalMs: totalMs, ErrorRate: errRate,
		})
		return true
	})

	// 按调用次数排序
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Calls > stats[j].Calls
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tools": stats,
		"total": len(stats),
	})
}

// handleResetToolAnalytics 重置工具使用统计
func (s *HTTPServer) handleResetToolAnalytics(w http.ResponseWriter, r *http.Request) {
	count := 0
	s.toolUsage.Range(func(key, _ interface{}) bool {
		s.toolUsage.Delete(key)
		count++
		return true
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "reset",
		"detail": fmt.Sprintf("cleared %d tool stats", count),
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

// handleSearchMessages 在会话历史中搜索消息
func (s *HTTPServer) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter q is required"})
		return
	}

	roleFilter := r.URL.Query().Get("role")
	queryLower := strings.ToLower(query)

	type matchResult struct {
		Index   int    `json:"index"`
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	var matches []matchResult
	for i, msg := range sess.agent.GetHistory() {
		if roleFilter != "" && string(msg.Role) != roleFilter {
			continue
		}
		if strings.Contains(strings.ToLower(msg.Content), queryLower) {
			excerpt := msg.Content
			if len(excerpt) > 200 {
				excerpt = excerpt[:200] + "..."
			}
			matches = append(matches, matchResult{
				Index: i, Role: string(msg.Role), Content: excerpt,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": sid,
		"query":   query,
		"matches": matches,
		"total":   len(matches),
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

// handleCompareSessions 比较两个会话
func (s *HTTPServer) handleCompareSessions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session1 string `json:"session1"`
		Session2 string `json:"session2"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Session1 == "" || req.Session2 == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session1 and session2 are required"})
		return
	}

	val1, ok1 := s.sessions.Load(req.Session1)
	val2, ok2 := s.sessions.Load(req.Session2)
	if !ok1 || !ok2 {
		missing := []string{}
		if !ok1 {
			missing = append(missing, req.Session1)
		}
		if !ok2 {
			missing = append(missing, req.Session2)
		}
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"error":   "session(s) not found",
			"missing": missing,
		})
		return
	}

	sess1 := val1.(*httpSession)
	sess2 := val2.(*httpSession)
	hist1 := sess1.agent.GetHistory()
	hist2 := sess2.agent.GetHistory()

	// 统计共享前缀长度
	commonPrefix := 0
	minLen := len(hist1)
	if len(hist2) < minLen {
		minLen = len(hist2)
	}
	for i := 0; i < minLen; i++ {
		if hist1[i].Content == hist2[i].Content && hist1[i].Role == hist2[i].Role {
			commonPrefix++
		} else {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session1": map[string]interface{}{
			"id":       req.Session1,
			"messages": len(hist1),
			"locked":   sess1.locked,
		},
		"session2": map[string]interface{}{
			"id":       req.Session2,
			"messages": len(hist2),
			"locked":   sess2.locked,
		},
		"common_prefix_length": commonPrefix,
		"diverged_at":          commonPrefix,
	})
}

// handleSessionSummary 生成会话摘要统计
func (s *HTTPServer) handleSessionSummary(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session")
	val, ok := s.sessions.Load(sid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	sess := val.(*httpSession)
	hist := sess.agent.GetHistory()

	if len(hist) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session":  sid,
			"summary":  "empty conversation",
			"messages": 0,
		})
		return
	}

	// 统计
	userMsgs := 0
	assistantMsgs := 0
	toolCalls := 0
	totalUserChars := 0
	totalAssistantChars := 0
	var firstUserMsg, lastUserMsg string

	for _, msg := range hist {
		switch string(msg.Role) {
		case "user":
			userMsgs++
			totalUserChars += len(msg.Content)
			if firstUserMsg == "" {
				firstUserMsg = msg.Content
			}
			lastUserMsg = msg.Content
		case "assistant":
			assistantMsgs++
			totalAssistantChars += len(msg.Content)
		case "tool":
			toolCalls++
		}
	}

	// 截断摘要
	if len(firstUserMsg) > 100 {
		firstUserMsg = firstUserMsg[:100] + "..."
	}
	if len(lastUserMsg) > 100 {
		lastUserMsg = lastUserMsg[:100] + "..."
	}

	avgUserLen := 0
	if userMsgs > 0 {
		avgUserLen = totalUserChars / userMsgs
	}
	avgAssistantLen := 0
	if assistantMsgs > 0 {
		avgAssistantLen = totalAssistantChars / assistantMsgs
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session":              sid,
		"total_messages":       len(hist),
		"user_messages":        userMsgs,
		"assistant_messages":   assistantMsgs,
		"tool_calls":           toolCalls,
		"first_user_message":   firstUserMsg,
		"last_user_message":    lastUserMsg,
		"avg_user_msg_length":  avgUserLen,
		"avg_assistant_msg_length": avgAssistantLen,
		"total_chars":          totalUserChars + totalAssistantChars,
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
