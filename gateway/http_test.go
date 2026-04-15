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
