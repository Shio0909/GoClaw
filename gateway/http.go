package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/rag"
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

	server *http.Server
}

type httpSession struct {
	agent    *agent.Agent
	memMgr   *memory.Manager
	lastUsed time.Time
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

	return srv
}

func (s *HTTPServer) Name() string { return "http" }

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
		<-ctx.Done()
		s.saveAllSessions() // 关闭前持久化所有会话
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// withRequestLog 记录每个请求的方法、路径和耗时
func (s *HTTPServer) withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := requestCounter.Add(1)
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		log.Printf("[HTTP] #%d %s %s → %d (%v)", reqID, r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
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
	writeJSON(w, http.StatusOK, map[string]string{"message": "session deleted"})
}

func (s *HTTPServer) handleListTools(w http.ResponseWriter, r *http.Request) {
	names := s.registry.Names()
	type toolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	toolList := make([]toolInfo, 0, len(names))
	for _, name := range names {
		t, _ := s.registry.Get(name)
		if t != nil {
			toolList = append(toolList, toolInfo{Name: t.Name, Description: t.Description})
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"gateway":  s.Name(),
		"provider": s.agentCfg.Provider,
		"model":    s.agentCfg.Model,
		"tools":    len(s.registry.Names()),
	})
}

func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	sessionCount := 0
	s.sessions.Range(func(_, _ any) bool {
		sessionCount++
		return true
	})

	uptime := time.Since(s.startedAt)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_seconds":  int(uptime.Seconds()),
		"uptime_human":    uptime.Round(time.Second).String(),
		"total_requests":  requestCounter.Load(),
		"total_chats":     s.chatCount.Load(),
		"active_sessions": sessionCount,
		"tools_loaded":    len(s.registry.Names()),
		"model":           s.agentCfg.Model,
		"provider":        s.agentCfg.Provider,
		"tool_stats":      tools.GetGlobalToolStats().Summary(),
	})
}

// handleListSessions 列出所有活跃会话
func (s *HTTPServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	type sessionInfo struct {
		ID          string `json:"id"`
		MessageCount int   `json:"message_count"`
		LastUsed    string `json:"last_used"`
		IdleSeconds int    `json:"idle_seconds"`
	}

	var sessions []sessionInfo
	s.sessions.Range(func(key, val any) bool {
		id := key.(string)
		sess := val.(*httpSession)
		idle := time.Since(sess.lastUsed)
		sessions = append(sessions, sessionInfo{
			ID:           id,
			MessageCount: len(sess.agent.GetHistory()),
			LastUsed:     sess.lastUsed.Format(time.RFC3339),
			IdleSeconds:  int(idle.Seconds()),
		})
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
				if time.Since(sess.lastUsed) > s.sessionTimeout {
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

// -------- 工具函数 --------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
