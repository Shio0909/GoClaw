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
	return &HTTPServer{
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
	}
}

func (s *HTTPServer) Name() string { return "http" }

// Run 启动 HTTP 服务器，阻塞直到 ctx 取消
func (s *HTTPServer) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", s.handleChat)
	mux.HandleFunc("GET /v1/chat/{session}", s.handleGetHistory)
	mux.HandleFunc("DELETE /v1/chat/{session}", s.handleDeleteSession)
	mux.HandleFunc("GET /v1/tools", s.handleListTools)
	mux.HandleFunc("GET /v1/memory/{session}", s.handleGetMemory)
	mux.HandleFunc("GET /v1/health", s.handleHealth)

	// 中间件链：CORS → 请求日志 → 认证
	handler := s.withAuth(s.withRequestLog(mux))
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
					s.sessions.Delete(key)
					log.Printf("[HTTP] 清理过期会话: %s", key)
				}
				return true
			})
		case <-ctx.Done():
			return
		}
	}
}

// -------- 工具函数 --------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
