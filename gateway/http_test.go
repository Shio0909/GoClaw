package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/audit"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
	"github.com/goclaw/goclaw/webhook"
)

func newTestHTTPServer(t *testing.T) *HTTPServer {
	t.Helper()
	store := memory.NewStore(t.TempDir())
	registry := tools.NewRegistry()
	return NewHTTPServer(HTTPServerConfig{
		Addr:     ":0",
		AgentCfg: agent.Config{Provider: "openai", APIKey: "test-key", BaseURL: "http://fake", Model: "test"},
		Registry: registry,
		MemStore: store,
	})
}

func TestHealthEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", srv.handleHealth)

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
	if resp["gateway"] != "http" {
		t.Errorf("expected gateway=http, got %v", resp["gateway"])
	}
}

func TestListToolsEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	// 注册一个测试工具
	srv.registry.Register(&tools.ToolDef{
		Name:        "test_tool",
		Description: "A test tool",
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tools", srv.handleListTools)

	req := httptest.NewRequest("GET", "/v1/tools", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := resp["count"].(float64)
	if count != 1 {
		t.Errorf("expected 1 tool, got %v", count)
	}
}

func TestChatEndpointValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", srv.handleChat)

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"empty body", "{}", 400},
		{"missing session", `{"message":"hi"}`, 400},
		{"missing message", `{"session":"s1"}`, 400},
		{"invalid json", "not json", 400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Errorf("expected %d, got %d: %s", tt.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestAuthMiddleware(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.apiToken = "secret-token"

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})
	handler := srv.withAuth(inner)

	// No token
	req := httptest.NewRequest("GET", "/v1/tools", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", w.Code)
	}

	// Wrong token
	req = httptest.NewRequest("GET", "/v1/tools", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", w.Code)
	}

	// Correct token
	req = httptest.NewRequest("GET", "/v1/tools", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with correct token, got %d", w.Code)
	}

	// Health endpoint bypasses auth
	req = httptest.NewRequest("GET", "/v1/health", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for health (no auth), got %d", w.Code)
	}
}

func TestSessionDeleteEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/chat/{session}", srv.handleDeleteSession)

	// Delete non-existent
	req := httptest.NewRequest("DELETE", "/v1/chat/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}

	// Create then delete
	srv.getOrCreateSession("test-session")
	req = httptest.NewRequest("DELETE", "/v1/chat/test-session", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestMemoryEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/memory/{session}", srv.handleGetMemory)

	req := httptest.NewRequest("GET", "/v1/memory/default", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["soul_length"]; !ok {
		t.Error("expected soul_length in response")
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	// apiToken is empty, auth should be skipped
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	})
	handler := srv.withAuth(inner)

	req := httptest.NewRequest("GET", "/v1/tools", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when no token configured, got %d", w.Code)
	}
}

func TestCORSMiddleware(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.corsOrigins = []string{"*"}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := srv.withCORS(inner)

	// Preflight OPTIONS
	req := httptest.NewRequest("OPTIONS", "/v1/chat", nil)
	req.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS origin *, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}

	// Normal request with CORS
	req = httptest.NewRequest("GET", "/v1/health", nil)
	req.Header.Set("Origin", "http://example.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS header on response")
	}
}

func TestCORSSpecificOrigins(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.corsOrigins = []string{"http://allowed.com", "http://also-ok.com"}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := srv.withCORS(inner)

	// Allowed origin
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://allowed.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "http://allowed.com" {
		t.Errorf("expected specific origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}

	// Disallowed origin
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "http://evil.com")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("expected no CORS header for disallowed origin, got %q", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestRequestLogMiddleware(t *testing.T) {
	srv := newTestHTTPServer(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := srv.withRequestLog(inner)

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	// Should generate X-Request-ID when not provided
	xrid := w.Header().Get("X-Request-ID")
	if xrid == "" {
		t.Error("expected X-Request-ID in response header")
	}
	if !strings.HasPrefix(xrid, "goclaw-") {
		t.Errorf("auto-generated X-Request-ID should start with goclaw-, got %s", xrid)
	}
}

func TestRequestIDPassthrough(t *testing.T) {
	srv := newTestHTTPServer(t)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := srv.withRequestLog(inner)

	req := httptest.NewRequest("GET", "/v1/health", nil)
	req.Header.Set("X-Request-ID", "custom-trace-abc123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	xrid := w.Header().Get("X-Request-ID")
	if xrid != "custom-trace-abc123" {
		t.Errorf("expected passthrough X-Request-ID, got %s", xrid)
	}
}

func TestHealthEndpointEnhanced(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.fallbackCfg = &agent.FallbackConfig{Model: "gpt-4o-mini", Provider: "openai"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", srv.handleHealth)

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["uptime_seconds"]; !ok {
		t.Error("expected uptime_seconds in health response")
	}
	if _, ok := resp["active_sessions"]; !ok {
		t.Error("expected active_sessions in health response")
	}
	if resp["fallback_model"] != "gpt-4o-mini" {
		t.Errorf("expected fallback_model gpt-4o-mini, got %v", resp["fallback_model"])
	}
}

func TestChatCompletionsValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", srv.handleChatCompletions)

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"empty messages", `{"messages":[]}`, 400},
		{"no user msg", `{"messages":[{"role":"system","content":"hi"}]}`, 400},
		{"invalid json", "bad json", 400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Errorf("expected %d, got %d: %s", tt.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestModelsEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", srv.handleModels)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["object"] != "list" {
		t.Errorf("expected object=list, got %v", resp["object"])
	}
	data := resp["data"].([]interface{})
	if len(data) != 1 {
		t.Errorf("expected 1 model, got %d", len(data))
	}
}

func TestHashStr(t *testing.T) {
	h1 := hashStr("hello")
	h2 := hashStr("hello")
	h3 := hashStr("world")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/metrics", srv.handleMetrics)

	req := httptest.NewRequest("GET", "/v1/metrics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if _, ok := resp["uptime_seconds"]; !ok {
		t.Error("expected uptime_seconds in response")
	}
	if _, ok := resp["active_sessions"]; !ok {
		t.Error("expected active_sessions in response")
	}
	if _, ok := resp["total_chats"]; !ok {
		t.Error("expected total_chats in response")
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/metrics", srv.handleMetrics)

	req := httptest.NewRequest("GET", "/v1/metrics?format=prometheus", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "goclaw_uptime_seconds") {
		t.Error("expected goclaw_uptime_seconds in prometheus output")
	}
	if !strings.Contains(body, "goclaw_requests_total") {
		t.Error("expected goclaw_requests_total in prometheus output")
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("expected Prometheus TYPE annotations")
	}
}

func TestListSessionsEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions", srv.handleListSessions)

	req := httptest.NewRequest("GET", "/v1/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := resp["count"].(float64)
	if count != 0 {
		t.Errorf("expected 0 sessions, got %v", count)
	}
	sessions := resp["sessions"].([]interface{})
	if len(sessions) != 0 {
		t.Errorf("expected empty sessions array, got %d", len(sessions))
	}
}

func TestListSessionsWithSessions(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.getOrCreateSession("session-1")
	srv.getOrCreateSession("session-2")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions", srv.handleListSessions)

	req := httptest.NewRequest("GET", "/v1/sessions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := resp["count"].(float64)
	if count != 2 {
		t.Errorf("expected 2 sessions, got %v", count)
	}
}

func TestForkSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.getOrCreateSession("source-session")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork", srv.handleForkSession)

	body := `{"new_session":"forked-session"}`
	req := httptest.NewRequest("POST", "/v1/sessions/source-session/fork", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["source"] != "source-session" {
		t.Errorf("expected source=source-session, got %v", resp["source"])
	}
	if resp["new_session"] != "forked-session" {
		t.Errorf("expected new_session=forked-session, got %v", resp["new_session"])
	}

	// 验证新会话已创建
	if _, ok := srv.sessions.Load("forked-session"); !ok {
		t.Error("forked session not found")
	}
}

func TestForkSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork", srv.handleForkSession)

	body := `{"new_session":"new"}`
	req := httptest.NewRequest("POST", "/v1/sessions/nonexistent/fork", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestForkSessionConflict(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.getOrCreateSession("source")
	srv.getOrCreateSession("already-exists")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork", srv.handleForkSession)

	body := `{"new_session":"already-exists"}`
	req := httptest.NewRequest("POST", "/v1/sessions/source/fork", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestGetConfig(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/config", srv.handleGetConfig)

	req := httptest.NewRequest("GET", "/v1/config", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	cfg := resp["config"].(map[string]interface{})
	if cfg["provider"] != "openai" {
		t.Errorf("expected provider openai, got %v", cfg["provider"])
	}
	apiKey := cfg["api_key"].(string)
	if apiKey == "test-key" {
		t.Error("API key should be masked, not raw")
	}
	if apiKey != "***" && !strings.Contains(apiKey, "...") {
		t.Error("masked API key should be *** or contain ...")
	}

	features := resp["features"].(map[string]interface{})
	if features == nil {
		t.Error("expected features in config response")
	}
}

func TestExportSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat/{session}/export", srv.handleExportSession)

	req := httptest.NewRequest("GET", "/v1/chat/nonexistent/export", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestExportSessionJSON(t *testing.T) {
	srv := newTestHTTPServer(t)
	ag := srv.getOrCreateSession("export-test")
	_ = ag

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat/{session}/export", srv.handleExportSession)

	req := httptest.NewRequest("GET", "/v1/chat/export-test/export", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["session"] != "export-test" {
		t.Errorf("expected session=export-test, got %v", resp["session"])
	}
}

func TestExportSessionMarkdown(t *testing.T) {
	srv := newTestHTTPServer(t)
	ag := srv.getOrCreateSession("md-test")
	_ = ag

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat/{session}/export", srv.handleExportSession)

	req := httptest.NewRequest("GET", "/v1/chat/md-test/export?format=markdown", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/markdown") {
		t.Errorf("expected markdown content type, got %s", contentType)
	}
}

func TestWebSocketPingPong(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", srv.handleWebSocket)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Send ping
	msg, _ := json.Marshal(wsMessage{Type: "ping"})
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read pong
	_, respBytes, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp wsResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Type != "pong" {
		t.Errorf("expected pong, got %s", resp.Type)
	}
}

func TestWebSocketInvalidJSON(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", srv.handleWebSocket)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, respBytes, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp wsResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Type != "error" {
		t.Errorf("expected error, got %s", resp.Type)
	}
}

func TestWebSocketClearSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	// 预创建一个会话
	srv.getOrCreateSession("ws-clear-test")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ws", srv.handleWebSocket)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg, _ := json.Marshal(wsMessage{Type: "clear", Session: "ws-clear-test"})
	if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, respBytes, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp wsResponse
	json.Unmarshal(respBytes, &resp)
	if resp.Type != "done" {
		t.Errorf("expected done, got %s", resp.Type)
	}
}

// ====== Config Reload Tests ======

func TestConfigReloadNoPath(t *testing.T) {
	srv := newTestHTTPServer(t) // configPath is empty
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/config/reload", srv.handleConfigReload)

	req := httptest.NewRequest("POST", "/v1/config/reload", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestConfigReloadBadPath(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.configPath = "/nonexistent/path/config.yaml"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/config/reload", srv.handleConfigReload)

	req := httptest.NewRequest("POST", "/v1/config/reload", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// config.Load returns empty config on missing file (no error), so we expect 200 with 0 changes
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestConfigReloadSuccess(t *testing.T) {
	dir := t.TempDir()
	cfgFile := dir + "/test.yaml"

	// 写入测试配置
	cfgContent := `agent:
  provider: openai
  api_key: test-key
  model: gpt-4o
  max_step: 50
`
	if err := writeFile(cfgFile, cfgContent); err != nil {
		t.Fatal(err)
	}

	srv := newTestHTTPServer(t)
	srv.configPath = cfgFile

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/config/reload", srv.handleConfigReload)

	req := httptest.NewRequest("POST", "/v1/config/reload", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["reloaded"] != true {
		t.Fatal("expected reloaded=true")
	}

	// 验证 model 被更新
	if srv.agentCfg.Model != "gpt-4o" {
		t.Fatalf("model not updated: %s", srv.agentCfg.Model)
	}
	if srv.agentCfg.MaxStep != 50 {
		t.Fatalf("max_step not updated: %d", srv.agentCfg.MaxStep)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// ====== Session Search Tests ======

func TestSessionSearchNoQuery(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/search", srv.handleSessionSearch)

	req := httptest.NewRequest("GET", "/v1/sessions/search", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSessionSearchNoResults(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.getOrCreateSession("s1")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/search", srv.handleSessionSearch)

	req := httptest.NewRequest("GET", "/v1/sessions/search?q=nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count != 0 {
		t.Fatalf("expected 0 results, got %d", count)
	}
}

func TestSessionSearchWithResults(t *testing.T) {
	srv := newTestHTTPServer(t)
	ag := srv.getOrCreateSession("search-hit")
	ag.SetHistory([]*schema.Message{
		{Role: schema.User, Content: "hello world"},
		{Role: schema.Assistant, Content: "hi there"},
	})

	// 另一个不匹配的会话
	ag2 := srv.getOrCreateSession("search-miss")
	ag2.SetHistory([]*schema.Message{
		{Role: schema.User, Content: "goodbye"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/search", srv.handleSessionSearch)

	req := httptest.NewRequest("GET", "/v1/sessions/search?q=hello", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("expected 1 result, got %d", count)
	}

	results := resp["results"].([]interface{})
	first := results[0].(map[string]interface{})
	if first["session_id"] != "search-hit" {
		t.Fatalf("expected session_id=search-hit, got %v", first["session_id"])
	}
}

func TestSessionSearchCaseInsensitive(t *testing.T) {
	srv := newTestHTTPServer(t)
	ag := srv.getOrCreateSession("ci-test")
	ag.SetHistory([]*schema.Message{
		{Role: schema.User, Content: "Hello WORLD"},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/search", srv.handleSessionSearch)

	req := httptest.NewRequest("GET", "/v1/sessions/search?q=hello+world", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 1 {
		t.Fatal("case-insensitive search should match")
	}
}

func TestSessionSearchWithLimit(t *testing.T) {
	srv := newTestHTTPServer(t)
	for i := 0; i < 5; i++ {
		ag := srv.getOrCreateSession("limit-" + strings.Repeat("x", i+1))
		ag.SetHistory([]*schema.Message{
			{Role: schema.User, Content: "common keyword"},
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/search", srv.handleSessionSearch)

	req := httptest.NewRequest("GET", "/v1/sessions/search?q=common&limit=2", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count > 2 {
		t.Fatalf("expected <=2 results with limit, got %d", count)
	}
}

// ====== Audit Endpoint Tests ======

func TestAuditEndpointDisabled(t *testing.T) {
	srv := newTestHTTPServer(t) // auditLog is nil
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audit", srv.handleAuditQuery)

	req := httptest.NewRequest("GET", "/v1/audit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != false {
		t.Fatal("expected enabled=false when auditLog is nil")
	}
}

func TestAuditEndpointWithEvents(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.auditLog = audit.NewLog(100)

	srv.auditLog.Emit(audit.EventChatStart, "s1", "start", "127.0.0.1", nil)
	srv.auditLog.Emit(audit.EventToolCall, "s1", "shell", "127.0.0.1", nil)
	srv.auditLog.Emit(audit.EventChatEnd, "s1", "end", "127.0.0.1", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audit", srv.handleAuditQuery)

	req := httptest.NewRequest("GET", "/v1/audit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatal("expected enabled=true")
	}
	if int(resp["total"].(float64)) != 3 {
		t.Fatalf("expected total=3, got %v", resp["total"])
	}
	events := resp["events"].([]interface{})
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestAuditEndpointFilterByType(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.auditLog = audit.NewLog(100)

	srv.auditLog.Emit(audit.EventChatStart, "s1", "", "", nil)
	srv.auditLog.Emit(audit.EventToolCall, "s1", "shell", "", nil)
	srv.auditLog.Emit(audit.EventToolCall, "s1", "file_read", "", nil)
	srv.auditLog.Emit(audit.EventChatEnd, "s1", "", "", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audit", srv.handleAuditQuery)

	req := httptest.NewRequest("GET", "/v1/audit?type=tool_call", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	events := resp["events"].([]interface{})
	if len(events) != 2 {
		t.Fatalf("expected 2 tool_call events, got %d", len(events))
	}
}

func TestAuditEndpointSinceID(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.auditLog = audit.NewLog(100)

	srv.auditLog.Emit(audit.EventChatStart, "s1", "", "", nil)
	srv.auditLog.Emit(audit.EventChatEnd, "s1", "", "", nil)
	srv.auditLog.Emit(audit.EventError, "s1", "err", "", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/audit", srv.handleAuditQuery)

	req := httptest.NewRequest("GET", "/v1/audit?since_id=1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	events := resp["events"].([]interface{})
	if len(events) != 2 {
		t.Fatalf("expected 2 events after id=1, got %d", len(events))
	}
}

func TestClientIP(t *testing.T) {
	// Standard request
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	if ip := clientIP(req); ip != "192.168.1.1" {
		t.Fatalf("expected 192.168.1.1, got %s", ip)
	}

	// With X-Forwarded-For
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "proxy:5678"
	req2.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if ip := clientIP(req2); ip != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %s", ip)
	}
}

// ====== Webhook Endpoint Tests ======

func TestListWebhooksDisabled(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/webhooks", srv.handleListWebhooks)

	req := httptest.NewRequest("GET", "/v1/webhooks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != false {
		t.Fatal("expected enabled=false")
	}
}

func TestListWebhooksEnabled(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.webhookMgr = webhook.NewManager([]webhook.Hook{
		{URL: "http://example.com/hook"},
	})
	defer srv.webhookMgr.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/webhooks", srv.handleListWebhooks)

	req := httptest.NewRequest("GET", "/v1/webhooks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatal("expected enabled=true")
	}
	hooks := resp["hooks"].([]interface{})
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
}

func TestAddWebhook(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.webhookMgr = webhook.NewManager(nil)
	defer srv.webhookMgr.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/webhooks", srv.handleAddWebhook)

	body := `{"url":"http://new-hook.com/callback","events":["chat.complete"]}`
	req := httptest.NewRequest("POST", "/v1/webhooks", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	hooks := srv.webhookMgr.ListHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
}

func TestAddWebhookNoURL(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.webhookMgr = webhook.NewManager(nil)
	defer srv.webhookMgr.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/webhooks", srv.handleAddWebhook)

	body := `{"events":["chat.complete"]}`
	req := httptest.NewRequest("POST", "/v1/webhooks", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestRemoveWebhook(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.webhookMgr = webhook.NewManager([]webhook.Hook{
		{URL: "http://remove-me.com"},
	})
	defer srv.webhookMgr.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/webhooks", srv.handleRemoveWebhook)

	body := `{"url":"http://remove-me.com"}`
	req := httptest.NewRequest("DELETE", "/v1/webhooks", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	hooks := srv.webhookMgr.ListHooks()
	if len(hooks) != 0 {
		t.Fatalf("expected 0 hooks after removal, got %d", len(hooks))
	}
}

func TestRemoveWebhookNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.webhookMgr = webhook.NewManager(nil)
	defer srv.webhookMgr.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/webhooks", srv.handleRemoveWebhook)

	body := `{"url":"http://nonexistent.com"}`
	req := httptest.NewRequest("DELETE", "/v1/webhooks", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ====== Rate Limit Status Tests ======

func TestRateLimitStatusDisabled(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rate-limit", srv.handleRateLimitStatus)

	req := httptest.NewRequest("GET", "/v1/rate-limit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != false {
		t.Fatal("expected enabled=false")
	}
}

func TestRateLimitStatusEnabled(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.rateLimiter = NewRateLimiter(100, time.Minute)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rate-limit", srv.handleRateLimitStatus)

	req := httptest.NewRequest("GET", "/v1/rate-limit", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["enabled"] != true {
		t.Fatal("expected enabled=true")
	}
	if int(resp["rate_per_window"].(float64)) != 100 {
		t.Fatalf("expected rate=100, got %v", resp["rate_per_window"])
	}
	if int(resp["window_seconds"].(float64)) != 60 {
		t.Fatalf("expected window=60, got %v", resp["window_seconds"])
	}
}

// ====== Session Tags Tests ======

func TestSetAndGetTags(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/tags", srv.handleSetTags)
	mux.HandleFunc("GET /v1/sessions/{session}/tags", srv.handleGetTags)

	// 先创建会话
	srv.getOrCreateSession("tag-test")

	// 设置标签
	body := `{"tags":["prod","important"]}`
	req := httptest.NewRequest("PUT", "/v1/sessions/tag-test/tags", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 获取标签
	req = httptest.NewRequest("GET", "/v1/sessions/tag-test/tags", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	tags := resp["tags"].([]interface{})
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}
}

func TestDeleteTag(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/tags", srv.handleSetTags)
	mux.HandleFunc("DELETE /v1/sessions/{session}/tags", srv.handleDeleteTag)

	srv.getOrCreateSession("tag-del")

	// 设置标签
	body := `{"tags":["alpha","beta"]}`
	req := httptest.NewRequest("PUT", "/v1/sessions/tag-del/tags", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// 删除一个
	body = `{"tag":"alpha"}`
	req = httptest.NewRequest("DELETE", "/v1/sessions/tag-del/tags", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	tags := resp["tags"].([]interface{})
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag after delete, got %d", len(tags))
	}
}

func TestTagsSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/tags", srv.handleGetTags)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexistent/tags", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestListSessionsFilterByTag(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/tags", srv.handleSetTags)
	mux.HandleFunc("GET /v1/sessions", srv.handleListSessions)

	// 创建两个会话，只给一个打标
	srv.getOrCreateSession("s1")
	srv.getOrCreateSession("s2")

	body := `{"tags":["vip"]}`
	req := httptest.NewRequest("PUT", "/v1/sessions/s1/tags", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// 按 tag 过滤
	req = httptest.NewRequest("GET", "/v1/sessions?tag=vip", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("expected 1 session with tag vip, got %d", count)
	}
}

// ====== Session Annotations Tests ======

func TestAnnotateAndGetAnnotations(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/annotate", srv.handleAnnotateSession)
	mux.HandleFunc("GET /v1/sessions/{session}/annotations", srv.handleGetAnnotations)

	srv.getOrCreateSession("note-test")

	// 添加备注
	body := `{"text":"This session is for testing"}`
	req := httptest.NewRequest("POST", "/v1/sessions/note-test/annotate", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 再加一条
	body = `{"text":"Second note"}`
	req = httptest.NewRequest("POST", "/v1/sessions/note-test/annotate", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// 获取备注
	req = httptest.NewRequest("GET", "/v1/sessions/note-test/annotations", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Fatalf("expected 2 annotations, got %d", count)
	}
}

func TestAnnotateEmptyText(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/annotate", srv.handleAnnotateSession)

	srv.getOrCreateSession("note-empty")

	body := `{"text":"  "}`
	req := httptest.NewRequest("POST", "/v1/sessions/note-empty/annotate", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestAnnotationsSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/annotations", srv.handleGetAnnotations)

	req := httptest.NewRequest("GET", "/v1/sessions/ghost/annotations", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ====== Batch Chat Tests ======

func TestBatchChatMissingFields(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/batch/chat", srv.handleBatchChat)

	body := `{"sessions":[],"message":"hi"}`
	req := httptest.NewRequest("POST", "/v1/batch/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestBatchChatTooManySessions(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/batch/chat", srv.handleBatchChat)

	sessions := make([]string, 21)
	for i := range sessions {
		sessions[i] = fmt.Sprintf("s%d", i)
	}
	data, _ := json.Marshal(map[string]interface{}{
		"sessions": sessions,
		"message":  "test",
	})
	req := httptest.NewRequest("POST", "/v1/batch/chat", strings.NewReader(string(data)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ====== Admin GC Tests ======

func TestAdminGCNoExpired(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/gc", srv.handleAdminGC)

	// 创建一个新鲜的会话
	srv.getOrCreateSession("fresh")

	body := `{"max_idle_minutes":60}`
	req := httptest.NewRequest("POST", "/v1/admin/gc", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["cleaned"].(float64)) != 0 {
		t.Fatalf("expected 0 cleaned, got %v", resp["cleaned"])
	}
}

func TestAdminGCWithExpired(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/admin/gc", srv.handleAdminGC)

	// 创建一个过期会话
	srv.getOrCreateSession("old-session")
	if val, ok := srv.sessions.Load("old-session"); ok {
		sess := val.(*httpSession)
		sess.lastUsed = time.Now().Add(-2 * time.Hour)
	}

	body := `{"max_idle_minutes":30}`
	req := httptest.NewRequest("POST", "/v1/admin/gc", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["cleaned"].(float64)) != 1 {
		t.Fatalf("expected 1 cleaned, got %v", resp["cleaned"])
	}
}

// ====== Analytics Tests ======

func TestAnalyticsEndpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/analytics", srv.handleAnalytics)

	srv.getOrCreateSession("a1")
	srv.getOrCreateSession("a2")

	req := httptest.NewRequest("GET", "/v1/analytics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	sessions := resp["sessions"].(map[string]interface{})
	if int(sessions["active"].(float64)) != 2 {
		t.Fatalf("expected 2 active sessions, got %v", sessions["active"])
	}
	server := resp["server"].(map[string]interface{})
	if server["uptime_seconds"] == nil {
		t.Fatal("expected uptime_seconds in server")
	}
}

func TestAnalyticsWithAudit(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.auditLog = audit.NewLog(100)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/analytics", srv.handleAnalytics)

	req := httptest.NewRequest("GET", "/v1/analytics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["audit"] == nil {
		t.Fatal("expected audit counts in analytics")
	}
}

// ====== Deep Health Tests ======

func TestDeepHealthOK(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/deep", srv.handleDeepHealth)

	req := httptest.NewRequest("GET", "/v1/health/deep", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", resp["status"])
	}
	checks := resp["checks"].([]interface{})
	if len(checks) < 3 {
		t.Fatalf("expected at least 3 checks, got %d", len(checks))
	}
}

func TestDeepHealthNoAPIKey(t *testing.T) {
	store := memory.NewStore(t.TempDir())
	registry := tools.NewRegistry()
	srv := NewHTTPServer(HTTPServerConfig{
		Addr:     ":0",
		AgentCfg: agent.Config{Provider: "openai", BaseURL: "http://fake", Model: "test"},
		Registry: registry,
		MemStore: store,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/deep", srv.handleDeepHealth)

	req := httptest.NewRequest("GET", "/v1/health/deep", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	// model_config should be "warning"
	checks := resp["checks"].([]interface{})
	found := false
	for _, c := range checks {
		cm := c.(map[string]interface{})
		if cm["name"] == "model_config" && cm["status"] == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected model_config check with warning status")
	}
}

// ====== Tool Enable/Disable Tests ======

func TestDisableAndEnableTool(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.registry.Register(&tools.ToolDef{Name: "test_tool", Description: "test"})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/{name}/disable", srv.handleDisableTool)
	mux.HandleFunc("POST /v1/tools/{name}/enable", srv.handleEnableTool)
	mux.HandleFunc("GET /v1/tools/disabled", srv.handleListDisabledTools)
	mux.HandleFunc("GET /v1/tools", srv.handleListTools)

	// Disable
	req := httptest.NewRequest("POST", "/v1/tools/test_tool/disable", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check disabled list
	req = httptest.NewRequest("GET", "/v1/tools/disabled", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 1 {
		t.Fatalf("expected 1 disabled, got %v", resp["count"])
	}

	// Check tools list shows disabled flag
	req = httptest.NewRequest("GET", "/v1/tools", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	toolsList := resp["tools"].([]interface{})
	found := false
	for _, tl := range toolsList {
		tm := tl.(map[string]interface{})
		if tm["name"] == "test_tool" && tm["disabled"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("expected test_tool to show disabled=true")
	}

	// Enable
	req = httptest.NewRequest("POST", "/v1/tools/test_tool/enable", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check disabled list is empty
	req = httptest.NewRequest("GET", "/v1/tools/disabled", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 0 {
		t.Fatalf("expected 0 disabled after enable, got %v", resp["count"])
	}
}

func TestDisableToolNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/{name}/disable", srv.handleDisableTool)

	req := httptest.NewRequest("POST", "/v1/tools/nonexistent/disable", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ====== Latency Stats Tests ======

func TestLatencyStatsEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/latency", srv.handleLatencyStats)

	req := httptest.NewRequest("GET", "/v1/latency", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if int(resp["count"].(float64)) != 0 {
		t.Fatalf("expected 0 endpoints, got %v", resp["count"])
	}
}

func TestLatencyStatsRecorded(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.startedAt = time.Now()

	// 注册完整中间件链 + 端点
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", srv.handleHealth)
	mux.HandleFunc("GET /v1/latency", srv.handleLatencyStats)
	handler := srv.withRequestLog(mux)

	// 发几个请求
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/v1/health", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}

	// 查看延迟统计
	req := httptest.NewRequest("GET", "/v1/latency", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	endpoints := resp["endpoints"].([]interface{})
	if len(endpoints) == 0 {
		t.Fatal("expected at least 1 endpoint in latency stats")
	}

	// 找到 health 端点
	found := false
	for _, ep := range endpoints {
		em := ep.(map[string]interface{})
		if em["endpoint"] == "GET /v1/health" {
			if int(em["calls"].(float64)) != 3 {
				t.Fatalf("expected 3 calls, got %v", em["calls"])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected GET /v1/health in latency stats")
	}
}

// -------- Plugin Management Tests --------

func TestListPluginsNoManager(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/plugins", srv.handleListPlugins)

	req := httptest.NewRequest("GET", "/v1/plugins", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["enabled"] != false {
		t.Fatal("expected enabled=false when no plugin manager")
	}
}

func TestListPluginsWithManager(t *testing.T) {
	srv := newTestHTTPServer(t)
	dir := t.TempDir()
	pm := tools.NewPluginManager(dir)
	srv.pluginMgr = pm

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/plugins", srv.handleListPlugins)

	req := httptest.NewRequest("GET", "/v1/plugins", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["enabled"] != true {
		t.Fatal("expected enabled=true")
	}
	if int(resp["count"].(float64)) != 0 {
		t.Fatal("expected 0 plugins")
	}
}

func TestUnloadPluginNoManager(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/plugins/{name}", srv.handleUnloadPlugin)

	req := httptest.NewRequest("DELETE", "/v1/plugins/test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestReloadPluginsNoManager(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/plugins/reload", srv.handleReloadPlugins)

	req := httptest.NewRequest("POST", "/v1/plugins/reload", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -------- Cron Tests --------

func TestCronJobsEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/cron", srv.handleListCronJobs)

	req := httptest.NewRequest("GET", "/v1/cron", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 0 {
		t.Fatal("expected 0 jobs")
	}
}

func TestCronAddValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/cron", srv.handleAddCronJob)

	// missing session
	body := `{"message":"hello","interval_seconds":60}`
	req := httptest.NewRequest("POST", "/v1/cron", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing session, got %d", w.Code)
	}

	// interval too small
	body = `{"session":"s1","message":"hello","interval_seconds":5}`
	req = httptest.NewRequest("POST", "/v1/cron", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for small interval, got %d", w.Code)
	}
}

func TestCronAddAndDelete(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.requestTimeout = 5 * time.Second
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/cron", srv.handleAddCronJob)
	mux.HandleFunc("DELETE /v1/cron/{id}", srv.handleDeleteCronJob)
	mux.HandleFunc("GET /v1/cron", srv.handleListCronJobs)

	body := `{"session":"cron-test","message":"ping","interval_seconds":3600}`
	req := httptest.NewRequest("POST", "/v1/cron", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created map[string]interface{}
	json.NewDecoder(w.Body).Decode(&created)
	id := created["id"].(string)

	// list shows 1 job
	req = httptest.NewRequest("GET", "/v1/cron", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var listed map[string]interface{}
	json.NewDecoder(w.Body).Decode(&listed)
	if int(listed["count"].(float64)) != 1 {
		t.Fatalf("expected 1 job, got %v", listed["count"])
	}

	// delete
	req = httptest.NewRequest("DELETE", "/v1/cron/"+id, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// list empty again
	req = httptest.NewRequest("GET", "/v1/cron", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&listed)
	if int(listed["count"].(float64)) != 0 {
		t.Fatalf("expected 0 jobs after delete")
	}
}

func TestCronDeleteNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/cron/{id}", srv.handleDeleteCronJob)

	req := httptest.NewRequest("DELETE", "/v1/cron/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Disabled Tool Enforcement Test --------

func TestDisabledToolEnforced(t *testing.T) {
	srv := newTestHTTPServer(t)
	// Register a dummy tool
	srv.registry.Register(&tools.ToolDef{
		Name:        "dummy_tool",
		Description: "test tool",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "executed", nil
		},
	})

	// Before disabling, execute should work
	result, err := srv.registry.Execute(context.Background(), "dummy_tool", nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "executed" {
		t.Fatalf("expected 'executed', got %q", result)
	}

	// Disable the tool
	srv.disabledTools.Store("dummy_tool", true)

	// After disabling, execute should fail
	_, err = srv.registry.Execute(context.Background(), "dummy_tool", nil)
	if err == nil {
		t.Fatal("expected error for disabled tool")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got: %v", err)
	}

	// Re-enable
	srv.disabledTools.Delete("dummy_tool")
	result, err = srv.registry.Execute(context.Background(), "dummy_tool", nil)
	if err != nil {
		t.Fatalf("expected no error after re-enable, got %v", err)
	}
	if result != "executed" {
		t.Fatalf("expected 'executed' after re-enable, got %q", result)
	}
}

// -------- Session TTL Tests --------

func TestSetSessionTTL(t *testing.T) {
	srv := newTestHTTPServer(t)
	// create session
	srv.sessions.Store("ttl-test", &httpSession{
		agent:    nil,
		lastUsed: time.Now(),
	})

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/ttl", srv.handleSetSessionTTL)

	body := `{"ttl_minutes":120}`
	req := httptest.NewRequest("PUT", "/v1/sessions/ttl-test/ttl", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	sess, _ := srv.sessions.Load("ttl-test")
	if sess.(*httpSession).customTTL != 120*time.Minute {
		t.Fatalf("expected 120min TTL, got %v", sess.(*httpSession).customTTL)
	}
}

func TestSetSessionTTLNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/ttl", srv.handleSetSessionTTL)

	body := `{"ttl_minutes":60}`
	req := httptest.NewRequest("PUT", "/v1/sessions/nonexistent/ttl", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestSetSessionTTLValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.sessions.Store("s1", &httpSession{lastUsed: time.Now()})
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/ttl", srv.handleSetSessionTTL)

	// too small
	body := `{"ttl_minutes":0}`
	req := httptest.NewRequest("PUT", "/v1/sessions/s1/ttl", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ttl=0, got %d", w.Code)
	}
}

// -------- Tool Alias Tests --------

func TestToolAliasesCRUD(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.registry.Register(&tools.ToolDef{
		Name:        "file_read",
		Description: "read a file",
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "ok", nil
		},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tools/aliases", srv.handleListToolAliases)
	mux.HandleFunc("PUT /v1/tools/aliases", srv.handleSetToolAlias)
	mux.HandleFunc("DELETE /v1/tools/aliases/{alias}", srv.handleDeleteToolAlias)

	// empty list
	req := httptest.NewRequest("GET", "/v1/tools/aliases", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 0 {
		t.Fatal("expected 0 aliases")
	}

	// add alias
	body := `{"alias":"fr","tool":"file_read"}`
	req = httptest.NewRequest("PUT", "/v1/tools/aliases", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// list shows 1 alias
	req = httptest.NewRequest("GET", "/v1/tools/aliases", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if int(resp["count"].(float64)) != 1 {
		t.Fatal("expected 1 alias")
	}

	// delete alias
	req = httptest.NewRequest("DELETE", "/v1/tools/aliases/fr", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d", w.Code)
	}

	// delete nonexistent
	req = httptest.NewRequest("DELETE", "/v1/tools/aliases/nonexistent", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestToolAliasToolNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/tools/aliases", srv.handleSetToolAlias)

	body := `{"alias":"x","tool":"nonexistent"}`
	req := httptest.NewRequest("PUT", "/v1/tools/aliases", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Debug Routes Test --------

func TestDebugRoutes(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/debug/routes", srv.handleDebugRoutes)

	req := httptest.NewRequest("GET", "/v1/debug/routes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count < 40 {
		t.Fatalf("expected >= 40 routes, got %d", count)
	}
}

// -------- Env Info Test --------

func TestEnvInfo(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.startedAt = time.Now().Add(-10 * time.Second)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/env", srv.handleEnvInfo)

	req := httptest.NewRequest("GET", "/v1/env", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["go_version"] == nil {
		t.Fatal("expected go_version")
	}
	if resp["num_cpu"] == nil {
		t.Fatal("expected num_cpu")
	}
	if int(resp["uptime_seconds"].(float64)) < 10 {
		t.Fatal("expected uptime >= 10s")
	}
}

// -------- Session Rename Tests --------

func TestRenameSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.sessions.Store("old-id", &httpSession{lastUsed: time.Now()})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/rename", srv.handleRenameSession)

	body := `{"new_id":"new-id"}`
	req := httptest.NewRequest("POST", "/v1/sessions/old-id/rename", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// old should be gone
	if _, ok := srv.sessions.Load("old-id"); ok {
		t.Fatal("old session should be deleted")
	}
	// new should exist
	if _, ok := srv.sessions.Load("new-id"); !ok {
		t.Fatal("new session should exist")
	}
}

func TestRenameSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/rename", srv.handleRenameSession)

	body := `{"new_id":"x"}`
	req := httptest.NewRequest("POST", "/v1/sessions/nonexistent/rename", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestRenameSessionConflict(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.sessions.Store("a", &httpSession{lastUsed: time.Now()})
	srv.sessions.Store("b", &httpSession{lastUsed: time.Now()})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/rename", srv.handleRenameSession)

	body := `{"new_id":"b"}`
	req := httptest.NewRequest("POST", "/v1/sessions/a/rename", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// -------- Tool Dry-Run Tests --------

func TestToolDryRunValid(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.registry.Register(&tools.ToolDef{
		Name:        "my_tool",
		Description: "test",
		Parameters: []tools.ParamDef{
			{Name: "path", Type: "string", Required: true, Description: "file path"},
			{Name: "depth", Type: "number", Required: false},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "ok", nil
		},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/{name}/dry-run", srv.handleToolDryRun)

	body := `{"path":"/tmp/test"}`
	req := httptest.NewRequest("POST", "/v1/tools/my_tool/dry-run", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != true {
		t.Fatal("expected valid=true")
	}
	if resp["disabled"] != false {
		t.Fatal("expected disabled=false")
	}
}

func TestToolDryRunMissingRequired(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.registry.Register(&tools.ToolDef{
		Name: "req_tool",
		Parameters: []tools.ParamDef{
			{Name: "input", Type: "string", Required: true},
		},
		Fn: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return "ok", nil
		},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/{name}/dry-run", srv.handleToolDryRun)

	body := `{}`
	req := httptest.NewRequest("POST", "/v1/tools/req_tool/dry-run", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["valid"] != false {
		t.Fatal("expected valid=false for missing required params")
	}
	missing := resp["missing_required"].([]interface{})
	if len(missing) != 1 || missing[0] != "input" {
		t.Fatalf("expected missing=[input], got %v", missing)
	}
}

func TestToolDryRunNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/{name}/dry-run", srv.handleToolDryRun)

	req := httptest.NewRequest("POST", "/v1/tools/nonexistent/dry-run", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestLockUnlockSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/lock", srv.handleLockSession)
	mux.HandleFunc("POST /v1/sessions/{session}/unlock", srv.handleUnlockSession)

	// Create session
	sess := &httpSession{agent: srv.getOrCreateSession("lock-test")}
	srv.sessions.Store("lock-test", sess)

	// Lock
	req := httptest.NewRequest("POST", "/v1/sessions/lock-test/lock", strings.NewReader(`{"locked_by":"admin"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("lock: expected 200, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["locked_by"] != "admin" {
		t.Fatalf("expected locked_by=admin, got %s", resp["locked_by"])
	}

	// Lock again → conflict
	req = httptest.NewRequest("POST", "/v1/sessions/lock-test/lock", strings.NewReader(`{}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("double lock: expected 409, got %d", w.Code)
	}

	// Unlock
	req = httptest.NewRequest("POST", "/v1/sessions/lock-test/unlock", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unlock: expected 200, got %d", w.Code)
	}
	var unlockResp map[string]string
	json.NewDecoder(w.Body).Decode(&unlockResp)
	if unlockResp["previous_locker"] != "admin" {
		t.Fatalf("expected previous_locker=admin, got %s", unlockResp["previous_locker"])
	}

	// Unlock again → already unlocked
	req = httptest.NewRequest("POST", "/v1/sessions/lock-test/unlock", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("double unlock: expected 200, got %d", w.Code)
	}
}

func TestLockSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/lock", srv.handleLockSession)
	req := httptest.NewRequest("POST", "/v1/sessions/nonexistent/lock", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestLockedSessionBlocksChat(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", srv.handleChat)

	// Create and lock session
	ag := srv.getOrCreateSession("blocked-sess")
	sess := &httpSession{agent: ag, locked: true, lockedBy: "test"}
	srv.sessions.Store("blocked-sess", sess)

	body := `{"session":"blocked-sess","message":"hello"}`
	req := httptest.NewRequest("POST", "/v1/chat", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusLocked {
		t.Fatalf("expected 423 Locked, got %d", w.Code)
	}
}

func TestHTMLExport(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/export", srv.handleExportSession)

	// Create a session with some history
	ag := srv.getOrCreateSession("html-test")
	sess := &httpSession{agent: ag}
	srv.sessions.Store("html-test", sess)

	req := httptest.NewRequest("GET", "/v1/sessions/html-test/export?format=html", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html content type, got %s", ct)
	}
	respBody := w.Body.String()
	if !strings.Contains(respBody, "<!DOCTYPE html>") {
		t.Fatal("expected HTML doctype")
	}
	if !strings.Contains(respBody, "html-test") {
		t.Fatal("expected session ID in HTML")
	}
}

func TestEstimateCost(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/estimate-cost", srv.handleEstimateCost)

	body := `{"message":"Hello world, this is a test message","model":"gpt-4"}`
	req := httptest.NewRequest("POST", "/v1/estimate-cost", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["model"] != "gpt-4" {
		t.Fatalf("expected model=gpt-4, got %v", resp["model"])
	}
	if resp["currency"] != "USD" {
		t.Fatalf("expected currency=USD, got %v", resp["currency"])
	}
	if tokens, ok := resp["input_tokens"].(float64); !ok || tokens < 1 {
		t.Fatalf("expected input_tokens > 0, got %v", resp["input_tokens"])
	}
}

func TestEstimateCostEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/estimate-cost", srv.handleEstimateCost)

	body := `{"message":""}`
	req := httptest.NewRequest("POST", "/v1/estimate-cost", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSessionStats(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/stats", srv.handleSessionStats)

	ag := srv.getOrCreateSession("stats-test")
	sess := &httpSession{agent: ag}
	srv.sessions.Store("stats-test", sess)

	req := httptest.NewRequest("GET", "/v1/sessions/stats-test/stats", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["session"] != "stats-test" {
		t.Fatalf("expected session=stats-test, got %v", resp["session"])
	}
	if resp["locked"] != false {
		t.Fatalf("expected locked=false")
	}
}

func TestSessionStatsNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/stats", srv.handleSessionStats)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexistent/stats", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestBatchTools(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/batch", srv.handleBatchTools)

	body := `{"tools":[{"name":"echo","args":{"input":"hi"}}]}`
	req := httptest.NewRequest("POST", "/v1/tools/batch", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Fatalf("expected total=1, got %v", resp["total"])
	}
}

func TestBatchToolsEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/batch", srv.handleBatchTools)

	body := `{"tools":[]}`
	req := httptest.NewRequest("POST", "/v1/tools/batch", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestBatchToolsTooMany(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/batch", srv.handleBatchTools)

	// 21 tools
	tools := make([]map[string]interface{}, 21)
	for i := range tools {
		tools[i] = map[string]interface{}{"name": "echo", "args": map[string]interface{}{"input": "x"}}
	}
	data, _ := json.Marshal(map[string]interface{}{"tools": tools})
	req := httptest.NewRequest("POST", "/v1/tools/batch", strings.NewReader(string(data)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >20 tools, got %d", w.Code)
	}
}

func TestToolAnalytics(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tools/analytics", srv.handleToolAnalytics)
	mux.HandleFunc("DELETE /v1/tools/analytics", srv.handleResetToolAnalytics)

	// Add some usage data
	stats := &toolUsageStats{}
	stats.Calls.Store(10)
	stats.Errors.Store(2)
	stats.TotalMs.Store(500)
	srv.toolUsage.Store("echo", stats)

	// GET analytics
	req := httptest.NewRequest("GET", "/v1/tools/analytics", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Fatalf("expected 1 tool, got %v", resp["total"])
	}

	// DELETE (reset)
	req = httptest.NewRequest("DELETE", "/v1/tools/analytics", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d", w.Code)
	}

	// Verify empty
	req = httptest.NewRequest("GET", "/v1/tools/analytics", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 0 {
		t.Fatalf("expected 0 after reset, got %v", resp["total"])
	}
}

func TestPromptTemplatesCRUD(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/templates", srv.handleListTemplates)
	mux.HandleFunc("POST /v1/templates", srv.handleAddTemplate)
	mux.HandleFunc("DELETE /v1/templates/{name}", srv.handleDeleteTemplate)

	// List empty
	req := httptest.NewRequest("GET", "/v1/templates", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 0 {
		t.Fatalf("expected 0 templates")
	}

	// Add template with auto-variable extraction
	body := `{"name":"greeting","template":"Hello {{name}}, welcome to {{place}}!"}`
	req = httptest.NewRequest("POST", "/v1/templates", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("add: expected 200, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	tmpl := resp["template"].(map[string]interface{})
	vars := tmpl["variables"].([]interface{})
	if len(vars) != 2 {
		t.Fatalf("expected 2 variables, got %d", len(vars))
	}

	// List → 1
	req = httptest.NewRequest("GET", "/v1/templates", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Fatalf("expected 1 template")
	}

	// Delete
	req = httptest.NewRequest("DELETE", "/v1/templates/greeting", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", w.Code)
	}

	// Delete again → 404
	req = httptest.NewRequest("DELETE", "/v1/templates/greeting", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete again: expected 404, got %d", w.Code)
	}
}

func TestAddTemplateValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/templates", srv.handleAddTemplate)

	body := `{"name":"","template":""}`
	req := httptest.NewRequest("POST", "/v1/templates", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSearchMessages(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/search", srv.handleSearchMessages)

	ag := srv.getOrCreateSession("search-test")
	sess := &httpSession{agent: ag}
	srv.sessions.Store("search-test", sess)

	// Empty search param → 400
	req := httptest.NewRequest("GET", "/v1/sessions/search-test/search", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing q, got %d", w.Code)
	}

	// Valid search (empty history)
	req = httptest.NewRequest("GET", "/v1/sessions/search-test/search?q=hello", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 0 {
		t.Fatalf("expected 0 matches in empty history")
	}
}

func TestSearchMessagesNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/search", srv.handleSearchMessages)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexistent/search?q=test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestTrimMessages(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/trim", srv.handleTrimMessages)

	ag := srv.getOrCreateSession("trim-test")
	sess := &httpSession{agent: ag}
	srv.sessions.Store("trim-test", sess)

	// keep_last=0 → 400
	body := `{"keep_last":0}`
	req := httptest.NewRequest("POST", "/v1/sessions/trim-test/trim", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for keep_last=0, got %d", w.Code)
	}

	// Valid trim (nothing to trim)
	body = `{"keep_last":10}`
	req = httptest.NewRequest("POST", "/v1/sessions/trim-test/trim", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSystemPrompt(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/system-prompt", srv.handleSetSystemPrompt)
	mux.HandleFunc("GET /v1/sessions/{session}/system-prompt", srv.handleGetSystemPrompt)

	ag := srv.getOrCreateSession("sp-test")
	sess := &httpSession{agent: ag}
	srv.sessions.Store("sp-test", sess)

	// GET (no override)
	req := httptest.NewRequest("GET", "/v1/sessions/sp-test/system-prompt", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["has_override"] != false {
		t.Fatal("expected no override initially")
	}

	// SET
	setBody := `{"prompt":"You are a pirate."}`
	req = httptest.NewRequest("PUT", "/v1/sessions/sp-test/system-prompt", strings.NewReader(setBody))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set: expected 200, got %d", w.Code)
	}

	// GET again
	req = httptest.NewRequest("GET", "/v1/sessions/sp-test/system-prompt", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["prompt"] != "You are a pirate." {
		t.Fatalf("expected pirate prompt, got %v", resp["prompt"])
	}
	if resp["has_override"] != true {
		t.Fatal("expected has_override=true")
	}
}

func TestSystemPromptNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/system-prompt", srv.handleSetSystemPrompt)

	req := httptest.NewRequest("PUT", "/v1/sessions/nonexistent/system-prompt", strings.NewReader(`{"prompt":"x"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCompareSessions(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/compare", srv.handleCompareSessions)

	ag1 := srv.getOrCreateSession("cmp1")
	ag2 := srv.getOrCreateSession("cmp2")
	srv.sessions.Store("cmp1", &httpSession{agent: ag1})
	srv.sessions.Store("cmp2", &httpSession{agent: ag2})

	body := `{"session1":"cmp1","session2":"cmp2"}`
	req := httptest.NewRequest("POST", "/v1/sessions/compare", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["common_prefix_length"].(float64) != 0 {
		t.Fatalf("expected 0 common prefix for empty sessions")
	}
}

func TestCompareSessionsNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/compare", srv.handleCompareSessions)

	body := `{"session1":"exists","session2":"nope"}`
	srv.sessions.Store("exists", &httpSession{agent: srv.getOrCreateSession("exists")})
	req := httptest.NewRequest("POST", "/v1/sessions/compare", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestCompareSessionsValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/compare", srv.handleCompareSessions)

	body := `{"session1":"","session2":""}`
	req := httptest.NewRequest("POST", "/v1/sessions/compare", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestSessionSummary(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/summary", srv.handleSessionSummary)

	ag := srv.getOrCreateSession("sum-test")
	srv.sessions.Store("sum-test", &httpSession{agent: ag})

	req := httptest.NewRequest("GET", "/v1/sessions/sum-test/summary", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["session"] != "sum-test" {
		t.Fatalf("expected session=sum-test")
	}
	if resp["summary"] != "empty conversation" {
		t.Fatalf("expected empty conversation summary")
	}
}

func TestSessionSummaryNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/summary", srv.handleSessionSummary)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexistent/summary", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestImportSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/import", srv.handleImportSession)

	body := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}],"append":false}`
	req := httptest.NewRequest("POST", "/v1/sessions/import-test/import", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["imported"].(float64) != 2 {
		t.Fatalf("expected 2 imported, got %v", resp["imported"])
	}
	if resp["mode"] != "replace" {
		t.Fatalf("expected mode=replace, got %v", resp["mode"])
	}
}

func TestImportSessionInvalidRole(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/import", srv.handleImportSession)

	body := `{"messages":[{"role":"invalid","content":"test"}]}`
	req := httptest.NewRequest("POST", "/v1/sessions/import-test/import", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestImportSessionEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/import", srv.handleImportSession)

	body := `{"messages":[]}`
	req := httptest.NewRequest("POST", "/v1/sessions/import-test/import", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestInjectMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/inject", srv.handleInjectMessage)

	ag := srv.getOrCreateSession("inject-test")
	srv.sessions.Store("inject-test", &httpSession{agent: ag})

	body := `{"role":"user","content":"injected message"}`
	req := httptest.NewRequest("POST", "/v1/sessions/inject-test/inject", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["injected"] != true {
		t.Fatal("expected injected=true")
	}
	if resp["total"].(float64) != 1 {
		t.Fatalf("expected total=1, got %v", resp["total"])
	}
}

func TestInjectMessageNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/inject", srv.handleInjectMessage)

	body := `{"content":"test"}`
	req := httptest.NewRequest("POST", "/v1/sessions/nonexistent/inject", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestInjectMessageValidation(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/inject", srv.handleInjectMessage)

	ag := srv.getOrCreateSession("inject-val")
	srv.sessions.Store("inject-val", &httpSession{agent: ag})

	// Empty content
	body := `{"content":""}`
	req := httptest.NewRequest("POST", "/v1/sessions/inject-val/inject", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	// Invalid role
	body = `{"role":"invalid","content":"test"}`
	req = httptest.NewRequest("POST", "/v1/sessions/inject-val/inject", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid role, got %d", w.Code)
	}
}

// -------- Session Checkpoint Tests --------

func TestCreateCheckpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint", srv.handleCreateCheckpoint)
	mux.HandleFunc("GET /v1/sessions/{session}/checkpoints", srv.handleListCheckpoints)

	ag := srv.getOrCreateSession("cp-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	})
	srv.sessions.Store("cp-test", &httpSession{agent: ag})

	// Create checkpoint
	body := `{"name":"v1"}`
	req := httptest.NewRequest("POST", "/v1/sessions/cp-test/checkpoint", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["checkpoint"] != "v1" {
		t.Fatalf("expected checkpoint=v1, got %v", resp["checkpoint"])
	}
	if resp["messages"].(float64) != 2 {
		t.Fatalf("expected 2 messages in checkpoint, got %v", resp["messages"])
	}

	// List checkpoints
	req = httptest.NewRequest("GET", "/v1/sessions/cp-test/checkpoints", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Duplicate name
	req = httptest.NewRequest("POST", "/v1/sessions/cp-test/checkpoint", strings.NewReader(`{"name":"v1"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestRestoreCheckpoint(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint", srv.handleCreateCheckpoint)
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint/restore", srv.handleRestoreCheckpoint)

	ag := srv.getOrCreateSession("restore-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "msg1"},
	})
	srv.sessions.Store("restore-test", &httpSession{agent: ag})

	// Create checkpoint with 1 message
	req := httptest.NewRequest("POST", "/v1/sessions/restore-test/checkpoint", strings.NewReader(`{"name":"snap1"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("checkpoint create: expected 201, got %d", w.Code)
	}

	// Add more messages
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "msg2"},
		{Role: "user", Content: "msg3"},
	})

	// Restore checkpoint
	req = httptest.NewRequest("POST", "/v1/sessions/restore-test/checkpoint/restore", strings.NewReader(`{"name":"snap1"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d", w.Code)
	}

	// Verify history restored
	if len(ag.GetHistory()) != 1 {
		t.Fatalf("expected 1 message after restore, got %d", len(ag.GetHistory()))
	}

	// Restore non-existent
	req = httptest.NewRequest("POST", "/v1/sessions/restore-test/checkpoint/restore", strings.NewReader(`{"name":"nope"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing checkpoint, got %d", w.Code)
	}
}

func TestCheckpointNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/checkpoint", srv.handleCreateCheckpoint)
	mux.HandleFunc("GET /v1/sessions/{session}/checkpoints", srv.handleListCheckpoints)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/checkpoint", strings.NewReader(`{"name":"v1"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/v1/sessions/nonexist/checkpoints", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Message Edit / Delete / Undo Tests --------

func TestEditMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/messages/{index}", srv.handleEditMessage)

	ag := srv.getOrCreateSession("edit-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "original"},
	})
	srv.sessions.Store("edit-test", &httpSession{agent: ag})

	body := `{"content":"updated"}`
	req := httptest.NewRequest("PUT", "/v1/sessions/edit-test/messages/0", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	hist := ag.GetHistory()
	if hist[0].Content != "updated" {
		t.Fatalf("expected content=updated, got %s", hist[0].Content)
	}
}

func TestEditMessageOutOfRange(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/messages/{index}", srv.handleEditMessage)

	ag := srv.getOrCreateSession("edit-oor")
	srv.sessions.Store("edit-oor", &httpSession{agent: ag})

	req := httptest.NewRequest("PUT", "/v1/sessions/edit-oor/messages/5", strings.NewReader(`{"content":"x"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestDeleteMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/sessions/{session}/messages/{index}", srv.handleDeleteMessage)

	ag := srv.getOrCreateSession("del-msg")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	})
	srv.sessions.Store("del-msg", &httpSession{agent: ag})

	req := httptest.NewRequest("DELETE", "/v1/sessions/del-msg/messages/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["remaining"].(float64) != 2 {
		t.Fatalf("expected 2 remaining, got %v", resp["remaining"])
	}

	hist := ag.GetHistory()
	if len(hist) != 2 || hist[1].Content != "c" {
		t.Fatal("message not correctly deleted")
	}
}

func TestUndoMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/undo", srv.handleUndoMessage)

	ag := srv.getOrCreateSession("undo-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "second"},
	})
	srv.sessions.Store("undo-test", &httpSession{agent: ag})

	req := httptest.NewRequest("POST", "/v1/sessions/undo-test/undo", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["remaining"].(float64) != 1 {
		t.Fatalf("expected 1 remaining, got %v", resp["remaining"])
	}
}

func TestUndoMessageEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/undo", srv.handleUndoMessage)

	ag := srv.getOrCreateSession("undo-empty")
	srv.sessions.Store("undo-empty", &httpSession{agent: ag})

	req := httptest.NewRequest("POST", "/v1/sessions/undo-empty/undo", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -------- Session Clone Tests --------

func TestCloneSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/clone", srv.handleCloneSession)

	ag := srv.getOrCreateSession("src-clone")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	})
	srv.sessions.Store("src-clone", &httpSession{
		agent: ag,
		tags:  map[string]bool{"important": true},
	})

	body := `{"new_id":"my-clone"}`
	req := httptest.NewRequest("POST", "/v1/sessions/src-clone/clone", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["clone"] != "my-clone" {
		t.Fatalf("expected clone=my-clone, got %v", resp["clone"])
	}

	// Verify clone exists
	cloneVal, ok := srv.sessions.Load("my-clone")
	if !ok {
		t.Fatal("clone session not found")
	}
	cloneSess := cloneVal.(*httpSession)
	if len(cloneSess.agent.GetHistory()) != 2 {
		t.Fatalf("expected 2 messages in clone, got %d", len(cloneSess.agent.GetHistory()))
	}
	if !cloneSess.tags["important"] {
		t.Fatal("tags not copied to clone")
	}
}

func TestCloneSessionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/clone", srv.handleCloneSession)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/clone", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Bulk Delete Tests --------

func TestBulkDeleteSessions(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/bulk-delete", srv.handleBulkDeleteSessions)

	// Create sessions
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		ag := srv.getOrCreateSession(id)
		srv.sessions.Store(id, &httpSession{agent: ag})
	}

	body := `{"session_ids":["bd-1","bd-2","bd-nonexist"]}`
	req := httptest.NewRequest("POST", "/v1/sessions/bulk-delete", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["deleted"].(float64) != 2 {
		t.Fatalf("expected 2 deleted, got %v", resp["deleted"])
	}
	if resp["not_found"].(float64) != 1 {
		t.Fatalf("expected 1 not_found, got %v", resp["not_found"])
	}

	// Verify bd-3 still exists
	if _, ok := srv.sessions.Load("bd-3"); !ok {
		t.Fatal("bd-3 should still exist")
	}
}

func TestBulkDeleteEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/bulk-delete", srv.handleBulkDeleteSessions)

	req := httptest.NewRequest("POST", "/v1/sessions/bulk-delete", strings.NewReader(`{"session_ids":[]}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -------- Tool Pipeline Tests --------

func TestToolPipelineEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/pipeline", srv.handleToolPipeline)

	req := httptest.NewRequest("POST", "/v1/tools/pipeline", strings.NewReader(`{"steps":[]}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestToolPipelineTooMany(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/tools/pipeline", srv.handleToolPipeline)

	steps := make([]map[string]interface{}, 11)
	for i := range steps {
		steps[i] = map[string]interface{}{"tool": "test", "args": map[string]interface{}{}}
	}
	data, _ := json.Marshal(map[string]interface{}{"steps": steps})
	req := httptest.NewRequest("POST", "/v1/tools/pipeline", strings.NewReader(string(data)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for >10 steps, got %d", w.Code)
	}
}

// -------- Save Session Tests --------

func TestSaveSessionNoPersistence(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/save", srv.handleSaveSession)

	req := httptest.NewRequest("POST", "/v1/sessions/test/save", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no persistence, got %d", w.Code)
	}
}

// -------- Fork at Index Tests --------

func TestForkAtIndex(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork-at", srv.handleForkAtIndex)

	ag := srv.getOrCreateSession("fork-src")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
		{Role: "assistant", Content: "d"},
	})
	srv.sessions.Store("fork-src", &httpSession{agent: ag})

	body := `{"new_id":"fork-dst","at_index":2}`
	req := httptest.NewRequest("POST", "/v1/sessions/fork-src/fork-at", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["forked_messages"].(float64) != 2 {
		t.Fatalf("expected 2 forked messages, got %v", resp["forked_messages"])
	}

	// Verify fork has only 2 messages
	forkVal, ok := srv.sessions.Load("fork-dst")
	if !ok {
		t.Fatal("fork session not found")
	}
	forkSess := forkVal.(*httpSession)
	if len(forkSess.agent.GetHistory()) != 2 {
		t.Fatalf("expected 2 messages in fork, got %d", len(forkSess.agent.GetHistory()))
	}
}

func TestForkAtIndexNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork-at", srv.handleForkAtIndex)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/fork-at", strings.NewReader(`{"at_index":0}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestForkAtIndexOutOfRange(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/fork-at", srv.handleForkAtIndex)

	ag := srv.getOrCreateSession("fork-oor")
	ag.SetHistory([]*schema.Message{{Role: "user", Content: "a"}})
	srv.sessions.Store("fork-oor", &httpSession{agent: ag})

	body := `{"at_index":5}`
	req := httptest.NewRequest("POST", "/v1/sessions/fork-oor/fork-at", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// -------- Event Stream Tests --------

func TestEventStreamConnects(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events", srv.handleEventStream)

	req := httptest.NewRequest("GET", "/v1/events", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Fatalf("expected connected event, got: %s", body)
	}
}

// -------- Message Reaction Tests --------

func TestMessageReaction(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/react", srv.handleMessageReaction)
	mux.HandleFunc("GET /v1/sessions/{session}/messages/{index}/reactions", srv.handleGetReactions)

	ag := srv.getOrCreateSession("react-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello"},
	})
	srv.sessions.Store("react-test", &httpSession{agent: ag})

	// Add reaction
	body := `{"reaction":"👍"}`
	req := httptest.NewRequest("POST", "/v1/sessions/react-test/messages/0/react", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Get reactions
	req = httptest.NewRequest("GET", "/v1/sessions/react-test/messages/0/reactions", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	reactions := resp["reactions"].([]interface{})
	if len(reactions) != 1 || reactions[0] != "👍" {
		t.Fatalf("expected [👍], got %v", reactions)
	}
}

func TestMessageReactionNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/react", srv.handleMessageReaction)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/0/react", strings.NewReader(`{"reaction":"👍"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Session Archive Tests --------

func TestArchiveSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/archive", srv.handleArchiveSession)
	mux.HandleFunc("POST /v1/sessions/{session}/unarchive", srv.handleUnarchiveSession)
	mux.HandleFunc("GET /v1/sessions/archived", srv.handleListArchivedSessions)

	ag := srv.getOrCreateSession("arch-test")
	srv.sessions.Store("arch-test", &httpSession{agent: ag})

	// Archive
	req := httptest.NewRequest("POST", "/v1/sessions/arch-test/archive", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// List archived
	req = httptest.NewRequest("GET", "/v1/sessions/archived", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 1 {
		t.Fatalf("expected 1 archived, got %v", resp["count"])
	}

	// Unarchive
	req = httptest.NewRequest("POST", "/v1/sessions/arch-test/unarchive", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// List should be empty
	req = httptest.NewRequest("GET", "/v1/sessions/archived", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 0 {
		t.Fatalf("expected 0 archived after unarchive, got %v", resp["count"])
	}
}

func TestArchiveNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/archive", srv.handleArchiveSession)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/archive", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Messages Pagination Tests --------

func TestGetMessagesPagination(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/messages", srv.handleGetMessages)

	ag := srv.getOrCreateSession("page-test")
	msgs := make([]*schema.Message, 10)
	for i := range msgs {
		msgs[i] = &schema.Message{Role: "user", Content: fmt.Sprintf("msg-%d", i)}
	}
	ag.SetHistory(msgs)
	srv.sessions.Store("page-test", &httpSession{agent: ag})

	// Get first 3
	req := httptest.NewRequest("GET", "/v1/sessions/page-test/messages?offset=0&limit=3", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 10 {
		t.Fatalf("expected total=10, got %v", resp["total"])
	}
	if resp["has_more"] != true {
		t.Fatal("expected has_more=true")
	}
	msgArr := resp["messages"].([]interface{})
	if len(msgArr) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgArr))
	}
}

func TestGetMessagesWithRoleFilter(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/messages", srv.handleGetMessages)

	ag := srv.getOrCreateSession("role-filter")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	})
	srv.sessions.Store("role-filter", &httpSession{agent: ag})

	req := httptest.NewRequest("GET", "/v1/sessions/role-filter/messages?role=user", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 2 {
		t.Fatalf("expected 2 user messages, got %v", resp["total"])
	}
}

// -------- Token Count Tests --------

func TestTokenCount(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/tokens", srv.handleTokenCount)

	ag := srv.getOrCreateSession("token-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi there"},
	})
	srv.sessions.Store("token-test", &httpSession{agent: ag})

	req := httptest.NewRequest("GET", "/v1/sessions/token-test/tokens", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["messages"].(float64) != 2 {
		t.Fatalf("expected 2 messages, got %v", resp["messages"])
	}
	if resp["estimated_tokens"].(float64) <= 0 {
		t.Fatal("expected positive token estimate")
	}
}

func TestTokenCountNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/tokens", srv.handleTokenCount)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/tokens", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Uptime Tests --------

func TestUptime(t *testing.T) {
	srv := newTestHTTPServer(t)
	srv.startedAt = time.Now().Add(-5 * time.Minute)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/uptime", srv.handleUptime)

	req := httptest.NewRequest("GET", "/v1/uptime", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["uptime_seconds"].(float64) < 290 {
		t.Fatalf("expected ~300s uptime, got %v", resp["uptime_seconds"])
	}
}

// -------- Session Metadata Tests --------

func TestSessionMetadata(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/meta", srv.handleGetSessionMeta)
	mux.HandleFunc("PUT /v1/sessions/{session}/meta", srv.handleSetSessionMeta)

	ag := srv.getOrCreateSession("meta-test")
	srv.sessions.Store("meta-test", &httpSession{agent: ag})

	// Set metadata
	body := `{"project":"goclaw","version":"1.0"}`
	req := httptest.NewRequest("PUT", "/v1/sessions/meta-test/meta", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Get metadata
	req = httptest.NewRequest("GET", "/v1/sessions/meta-test/meta", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	meta := resp["metadata"].(map[string]interface{})
	if meta["project"] != "goclaw" {
		t.Fatalf("expected project=goclaw, got %v", meta["project"])
	}

	// Delete key by setting empty
	body = `{"project":""}`
	req = httptest.NewRequest("PUT", "/v1/sessions/meta-test/meta", strings.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	meta = resp["metadata"].(map[string]interface{})
	if _, exists := meta["project"]; exists {
		t.Fatal("expected project key deleted")
	}
}

func TestSessionMetaNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/meta", srv.handleGetSessionMeta)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/meta", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// -------- Bookmark Tests --------

func TestBookmarkMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/bookmark", srv.handleBookmarkMessage)
	mux.HandleFunc("GET /v1/sessions/{session}/bookmarks", srv.handleGetBookmarks)

	ag := srv.getOrCreateSession("bm-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "important question"},
		{Role: "assistant", Content: "critical answer"},
	})
	srv.sessions.Store("bm-test", &httpSession{agent: ag})

	// Bookmark message 1
	body := `{"label":"key-insight"}`
	req := httptest.NewRequest("POST", "/v1/sessions/bm-test/messages/1/bookmark", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// List bookmarks
	req = httptest.NewRequest("GET", "/v1/sessions/bm-test/bookmarks", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 1 {
		t.Fatalf("expected 1 bookmark, got %v", resp["count"])
	}
}

func TestBookmarkNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/bookmark", srv.handleBookmarkMessage)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/0/bookmark", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Star/Unstar Tests ────────

func TestStarSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/star", srv.handleStarSession)
	mux.HandleFunc("DELETE /v1/sessions/{session}/star", srv.handleUnstarSession)
	mux.HandleFunc("GET /v1/sessions/starred", srv.handleListStarredSessions)

	srv.getOrCreateSession("star-test")

	// Star
	req := httptest.NewRequest("POST", "/v1/sessions/star-test/star", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("star: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["starred"] != true {
		t.Errorf("expected starred=true")
	}

	// List starred
	req = httptest.NewRequest("GET", "/v1/sessions/starred", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list starred: expected 200, got %d", w.Code)
	}
	var listResp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&listResp)
	if listResp["count"].(float64) != 1 {
		t.Errorf("expected 1 starred, got %v", listResp["count"])
	}

	// Unstar
	req = httptest.NewRequest("DELETE", "/v1/sessions/star-test/star", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unstar: expected 200, got %d", w.Code)
	}
}

func TestStarNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/star", srv.handleStarSession)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/star", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Pin/Unpin Tests ────────

func TestPinMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/pin", srv.handlePinMessage)
	mux.HandleFunc("DELETE /v1/sessions/{session}/messages/{index}/pin", srv.handleUnpinMessage)
	mux.HandleFunc("GET /v1/sessions/{session}/pins", srv.handleGetPinnedMessages)

	ag := srv.getOrCreateSession("pin-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi there"},
	})

	// Pin message 0
	req := httptest.NewRequest("POST", "/v1/sessions/pin-test/messages/0/pin", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("pin: expected 200, got %d", w.Code)
	}

	// Get pinned
	req = httptest.NewRequest("GET", "/v1/sessions/pin-test/pins", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get pins: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 1 {
		t.Errorf("expected 1 pinned, got %v", resp["count"])
	}

	// Unpin
	req = httptest.NewRequest("DELETE", "/v1/sessions/pin-test/messages/0/pin", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unpin: expected 200, got %d", w.Code)
	}
}

func TestPinNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/pin", srv.handlePinMessage)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/0/pin", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestPinOutOfRange(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/pin", srv.handlePinMessage)

	srv.getOrCreateSession("pin-empty")

	req := httptest.NewRequest("POST", "/v1/sessions/pin-empty/messages/5/pin", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ──────── Markdown Export Tests ────────

func TestExportMarkdown(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/export/markdown", srv.handleExportMarkdown)

	ag := srv.getOrCreateSession("md-export")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "What is Go?"},
		{Role: "assistant", Content: "Go is a programming language."},
	})

	req := httptest.NewRequest("GET", "/v1/sessions/md-export/export/markdown", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# md-export") {
		t.Errorf("expected title in markdown")
	}
	if !strings.Contains(body, "👤 User") {
		t.Errorf("expected user role label")
	}
	if !strings.Contains(body, "🤖 Assistant") {
		t.Errorf("expected assistant role label")
	}
	if w.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Errorf("expected markdown content type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestExportMarkdownNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/export/markdown", srv.handleExportMarkdown)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/export/markdown", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Batch Export Tests ────────

func TestBatchExport(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/export", srv.handleBatchExport)

	ag1 := srv.getOrCreateSession("batch-1")
	ag1.SetHistory([]*schema.Message{{Role: "user", Content: "hello"}})
	ag2 := srv.getOrCreateSession("batch-2")
	ag2.SetHistory([]*schema.Message{{Role: "user", Content: "world"}})

	body := `{"sessions":["batch-1","batch-2","nonexist"]}`
	req := httptest.NewRequest("POST", "/v1/sessions/export", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 2 {
		t.Errorf("expected 2 exported, got %v", resp["count"])
	}
	notFound := resp["not_found"].([]interface{})
	if len(notFound) != 1 || notFound[0].(string) != "nonexist" {
		t.Errorf("expected not_found=[nonexist], got %v", notFound)
	}
}

func TestBatchExportEmpty(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/export", srv.handleBatchExport)

	req := httptest.NewRequest("POST", "/v1/sessions/export", strings.NewReader(`{"sessions":[]}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ──────── Branching Tests ────────

func TestCreateBranch(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/branch", srv.handleCreateBranch)
	mux.HandleFunc("GET /v1/sessions/{session}/branches", srv.handleListBranches)

	ag := srv.getOrCreateSession("branch-src")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "reply1"},
		{Role: "user", Content: "msg2"},
	})

	// Create branch at index 2
	body := `{"name":"alt-path","at_index":2}`
	req := httptest.NewRequest("POST", "/v1/sessions/branch-src/branch", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create branch: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["branch"] != "alt-path" {
		t.Errorf("expected branch name alt-path")
	}
	if resp["messages_copied"].(float64) != 2 {
		t.Errorf("expected 2 messages copied")
	}

	// List branches
	req = httptest.NewRequest("GET", "/v1/sessions/branch-src/branches", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list branches: expected 200, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 1 {
		t.Errorf("expected 1 branch")
	}
}

func TestCreateBranchDuplicate(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/branch", srv.handleCreateBranch)

	ag := srv.getOrCreateSession("dup-branch")
	ag.SetHistory([]*schema.Message{{Role: "user", Content: "test"}})
	val, _ := srv.sessions.Load("dup-branch")
	sess := val.(*httpSession)
	sess.branches = map[string]string{"existing": "some-id"}

	body := `{"name":"existing","at_index":1}`
	req := httptest.NewRequest("POST", "/v1/sessions/dup-branch/branch", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

// ──────── Global Search Tests ────────

func TestGlobalSearch(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/search/messages", srv.handleGlobalSearch)

	ag1 := srv.getOrCreateSession("search-1")
	ag1.SetHistory([]*schema.Message{
		{Role: "user", Content: "What is Kubernetes?"},
		{Role: "assistant", Content: "Kubernetes is a container orchestration platform."},
	})
	ag2 := srv.getOrCreateSession("search-2")
	ag2.SetHistory([]*schema.Message{
		{Role: "user", Content: "Tell me about Docker"},
	})

	// Search for "kubernetes"
	req := httptest.NewRequest("GET", "/v1/search/messages?q=kubernetes", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("expected 2 results for 'kubernetes', got %d", count)
	}

	// Search with no query
	req = httptest.NewRequest("GET", "/v1/search/messages", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty query, got %d", w.Code)
	}
}

// ──────── Session Merge Tests ────────

func TestMergeSessions(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/merge", srv.handleMergeSessions)

	ag1 := srv.getOrCreateSession("merge-a")
	ag1.SetHistory([]*schema.Message{{Role: "user", Content: "from A"}})
	ag2 := srv.getOrCreateSession("merge-b")
	ag2.SetHistory([]*schema.Message{{Role: "user", Content: "from B"}})

	body := `{"sources":["merge-a","merge-b"],"target":"merge-result"}`
	req := httptest.NewRequest("POST", "/v1/sessions/merge", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total_messages"].(float64) != 2 {
		t.Errorf("expected 2 total messages, got %v", resp["total_messages"])
	}
}

func TestMergeSessionsTargetExists(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/merge", srv.handleMergeSessions)

	srv.getOrCreateSession("existing")
	srv.getOrCreateSession("src1")
	srv.getOrCreateSession("src2")

	body := `{"sources":["src1","src2"],"target":"existing"}`
	req := httptest.NewRequest("POST", "/v1/sessions/merge", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w.Code)
	}
}

func TestMergeSessionsTooFewSources(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/merge", srv.handleMergeSessions)

	body := `{"sources":["only-one"],"target":"new"}`
	req := httptest.NewRequest("POST", "/v1/sessions/merge", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ──────── Auto-Title Tests ────────

func TestAutoTitle(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/auto-title", srv.handleAutoTitle)

	ag := srv.getOrCreateSession("title-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "How to deploy a Go application to production?"},
		{Role: "assistant", Content: "Here are the steps..."},
	})

	req := httptest.NewRequest("POST", "/v1/sessions/title-test/auto-title", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	title := resp["title"].(string)
	if title == "" {
		t.Errorf("expected non-empty title")
	}
	if !strings.Contains(title, "How to deploy") {
		t.Errorf("expected title from first user message, got %s", title)
	}
}

func TestAutoTitleLongMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/auto-title", srv.handleAutoTitle)

	ag := srv.getOrCreateSession("title-long")
	longMsg := strings.Repeat("A very long message about a complex topic ", 10)
	ag.SetHistory([]*schema.Message{{Role: "user", Content: longMsg}})

	req := httptest.NewRequest("POST", "/v1/sessions/title-long/auto-title", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	title := resp["title"].(string)
	if len(title) > 70 {
		t.Errorf("title too long: %d chars", len(title))
	}
}

func TestAutoTitleNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/auto-title", srv.handleAutoTitle)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/auto-title", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Timeline Tests ────────

func TestSessionTimeline(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/timeline", srv.handleSessionTimeline)

	srv.getOrCreateSession("timeline-test")

	req := httptest.NewRequest("GET", "/v1/sessions/timeline-test/timeline", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	total := int(resp["total"].(float64))
	if total < 1 {
		t.Errorf("expected at least 1 timeline event (created), got %d", total)
	}
	events := resp["events"].([]interface{})
	first := events[0].(map[string]interface{})
	if first["type"] != "created" {
		t.Errorf("expected first event type=created, got %s", first["type"])
	}
}

func TestSessionTimelineNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/timeline", srv.handleSessionTimeline)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/timeline", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Message Voting Tests ────────

func TestMessageVote(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/vote", srv.handleMessageVote)
	mux.HandleFunc("GET /v1/sessions/{session}/votes", srv.handleGetVotes)

	ag := srv.getOrCreateSession("vote-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "test"},
		{Role: "assistant", Content: "response"},
	})

	// Upvote message 1
	req := httptest.NewRequest("POST", "/v1/sessions/vote-test/messages/1/vote", strings.NewReader(`{"value":1}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("vote: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["score"].(float64) != 1 {
		t.Errorf("expected score=1")
	}

	// Upvote again
	req = httptest.NewRequest("POST", "/v1/sessions/vote-test/messages/1/vote", strings.NewReader(`{"value":1}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["score"].(float64) != 2 {
		t.Errorf("expected score=2")
	}

	// Downvote
	req = httptest.NewRequest("POST", "/v1/sessions/vote-test/messages/1/vote", strings.NewReader(`{"value":-1}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["score"].(float64) != 1 {
		t.Errorf("expected score=1 after downvote")
	}

	// Get votes
	req = httptest.NewRequest("GET", "/v1/sessions/vote-test/votes", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get votes: expected 200, got %d", w.Code)
	}
}

func TestMessageVoteInvalidValue(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/vote", srv.handleMessageVote)

	ag := srv.getOrCreateSession("vote-invalid")
	ag.SetHistory([]*schema.Message{{Role: "user", Content: "test"}})

	req := httptest.NewRequest("POST", "/v1/sessions/vote-invalid/messages/0/vote", strings.NewReader(`{"value":5}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid value, got %d", w.Code)
	}
}

func TestMessageVoteNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/vote", srv.handleMessageVote)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/0/vote", strings.NewReader(`{"value":1}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Category Tests ────────

func TestSetCategory(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/category", srv.handleSetCategory)
	mux.HandleFunc("GET /v1/sessions/categories", srv.handleListCategories)

	srv.getOrCreateSession("cat-test")

	// Set category
	req := httptest.NewRequest("PUT", "/v1/sessions/cat-test/category", strings.NewReader(`{"category":"coding"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set category: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["category"] != "coding" {
		t.Errorf("expected category=coding")
	}

	// List categories
	req = httptest.NewRequest("GET", "/v1/sessions/categories", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list categories: expected 200, got %d", w.Code)
	}
}

func TestSetCategoryNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/category", srv.handleSetCategory)

	req := httptest.NewRequest("PUT", "/v1/sessions/nonexist/category", strings.NewReader(`{"category":"x"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Threading Tests ────────

func TestReplyToMessage(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/reply", srv.handleReplyToMessage)
	mux.HandleFunc("GET /v1/sessions/{session}/messages/{index}/thread", srv.handleGetThread)

	ag := srv.getOrCreateSession("thread-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "original question"},
		{Role: "assistant", Content: "original answer"},
	})

	// Add reply
	body := `{"author":"reviewer","content":"great answer!"}`
	req := httptest.NewRequest("POST", "/v1/sessions/thread-test/messages/1/reply", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["thread_size"].(float64) != 1 {
		t.Errorf("expected thread_size=1")
	}

	// Get thread
	req = httptest.NewRequest("GET", "/v1/sessions/thread-test/messages/1/thread", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get thread: expected 200, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 1 {
		t.Errorf("expected 1 reply")
	}
	if resp["parent_preview"] == "" {
		t.Errorf("expected parent preview")
	}
}

func TestReplyEmptyContent(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/reply", srv.handleReplyToMessage)

	ag := srv.getOrCreateSession("reply-empty")
	ag.SetHistory([]*schema.Message{{Role: "user", Content: "test"}})

	req := httptest.NewRequest("POST", "/v1/sessions/reply-empty/messages/0/reply", strings.NewReader(`{"content":""}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestReplyNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/{index}/reply", srv.handleReplyToMessage)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/0/reply", strings.NewReader(`{"content":"hi"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Sharing Tests ────────

func TestShareSession(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/share", srv.handleCreateShareToken)
	mux.HandleFunc("GET /v1/shared/{token}", srv.handleViewSharedSession)
	mux.HandleFunc("DELETE /v1/sessions/{session}/share", srv.handleRevokeShareToken)

	ag := srv.getOrCreateSession("share-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	})

	// Create share token
	req := httptest.NewRequest("POST", "/v1/sessions/share-test/share", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create share: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	token := resp["token"].(string)
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// View shared session
	req = httptest.NewRequest("GET", "/v1/shared/"+token, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("view shared: expected 200, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["read_only"] != true {
		t.Errorf("expected read_only=true")
	}
	msgs := resp["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}

	// Duplicate token should fail
	req = httptest.NewRequest("POST", "/v1/sessions/share-test/share", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate share: expected 409, got %d", w.Code)
	}

	// Revoke
	req = httptest.NewRequest("DELETE", "/v1/sessions/share-test/share", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: expected 200, got %d", w.Code)
	}

	// Token should no longer work
	req = httptest.NewRequest("GET", "/v1/shared/"+token, nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoked token: expected 404, got %d", w.Code)
	}
}

func TestShareNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/share", srv.handleCreateShareToken)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/share", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestViewInvalidToken(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/shared/{token}", srv.handleViewSharedSession)

	req := httptest.NewRequest("GET", "/v1/shared/invalidtoken", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Quota Tests ────────

func TestSessionQuota(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/quota", srv.handleSetQuota)
	mux.HandleFunc("GET /v1/sessions/{session}/quota", srv.handleGetQuota)

	srv.getOrCreateSession("quota-test")

	// Set quota
	req := httptest.NewRequest("PUT", "/v1/sessions/quota-test/quota", strings.NewReader(`{"max_messages":100}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set quota: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["max_messages"].(float64) != 100 {
		t.Errorf("expected max_messages=100")
	}
	if resp["remaining"].(float64) != 100 {
		t.Errorf("expected remaining=100")
	}

	// Get quota
	req = httptest.NewRequest("GET", "/v1/sessions/quota-test/quota", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get quota: expected 200, got %d", w.Code)
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["unlimited"] != false {
		t.Errorf("expected unlimited=false")
	}
}

func TestQuotaNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/sessions/{session}/quota", srv.handleSetQuota)

	req := httptest.NewRequest("PUT", "/v1/sessions/nonexist/quota", strings.NewReader(`{"max_messages":10}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── HTML Export Tests ────────

func TestExportHTML(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/export/html", srv.handleExportHTML)

	ag := srv.getOrCreateSession("html-export")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "What is <b>Go</b>?"},
		{Role: "assistant", Content: "Go is great & fast."},
	})

	req := httptest.NewRequest("GET", "/v1/sessions/html-export/export/html", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("expected HTML doctype")
	}
	if !strings.Contains(body, "&lt;b&gt;Go&lt;/b&gt;") {
		t.Error("expected HTML-escaped content")
	}
	if !strings.Contains(body, "&amp; fast") {
		t.Error("expected ampersand escaped")
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("expected html content type, got %s", w.Header().Get("Content-Type"))
	}
}

func TestExportHTMLNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/export/html", srv.handleExportHTML)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/export/html", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Conversation Tree Tests ────────

func TestConversationTree(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/tree", srv.handleConversationTree)

	ag := srv.getOrCreateSession("tree-test")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	})

	req := httptest.NewRequest("GET", "/v1/sessions/tree-test/tree", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 3 {
		t.Errorf("expected 3 nodes, got %v", resp["total"])
	}
	nodes := resp["nodes"].([]interface{})
	first := nodes[0].(map[string]interface{})
	if first["role"] != "user" {
		t.Errorf("expected first node role=user")
	}
}

func TestConversationTreeNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{session}/tree", srv.handleConversationTree)

	req := httptest.NewRequest("GET", "/v1/sessions/nonexist/tree", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ──────── Batch Pin/Vote Tests ────────

func TestBatchPinMessages(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/batch-pin", srv.handleBatchPinMessages)
	mux.HandleFunc("GET /v1/sessions/{session}/pins", srv.handleGetPinnedMessages)

	ag := srv.getOrCreateSession("batch-pin")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "c"},
	})

	// Batch pin
	body := `{"indices":[0,2],"pin":true}`
	req := httptest.NewRequest("POST", "/v1/sessions/batch-pin/messages/batch-pin", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("batch pin: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["count"].(float64) != 2 {
		t.Errorf("expected 2 pinned, got %v", resp["count"])
	}
	if resp["action"] != "pinned" {
		t.Errorf("expected action=pinned")
	}
}

func TestBatchPinNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/batch-pin", srv.handleBatchPinMessages)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/batch-pin", strings.NewReader(`{"indices":[0],"pin":true}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestBatchVoteMessages(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/batch-vote", srv.handleBatchVoteMessages)

	ag := srv.getOrCreateSession("batch-vote")
	ag.SetHistory([]*schema.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
	})

	body := `{"votes":[{"index":0,"value":1},{"index":1,"value":-1},{"index":2,"value":1}]}`
	req := httptest.NewRequest("POST", "/v1/sessions/batch-vote/messages/batch-vote", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("batch vote: expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["applied"].(float64) != 3 {
		t.Errorf("expected 3 applied, got %v", resp["applied"])
	}
}

func TestBatchVoteNotFound(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions/{session}/messages/batch-vote", srv.handleBatchVoteMessages)

	req := httptest.NewRequest("POST", "/v1/sessions/nonexist/messages/batch-vote", strings.NewReader(`{"votes":[{"index":0,"value":1}]}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
