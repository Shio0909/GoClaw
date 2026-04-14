package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/goclaw/goclaw/agent"
	"github.com/goclaw/goclaw/memory"
	"github.com/goclaw/goclaw/tools"
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
