package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAPISpec(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/openapi.json", srv.handleOpenAPISpec)

	req := httptest.NewRequest("GET", "/v1/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if spec["openapi"] != "3.0.3" {
		t.Errorf("expected openapi 3.0.3, got %v", spec["openapi"])
	}

	info := spec["info"].(map[string]interface{})
	if info["title"] == nil || info["title"] == "" {
		t.Error("expected title in info")
	}

	paths := spec["paths"].(map[string]interface{})
	expectedPaths := []string{
		"/v1/chat",
		"/v1/health",
		"/v1/metrics",
		"/v1/tools",
		"/v1/sessions",
		"/v1/config",
		"/v1/ws",
		"/v1/models",
		"/v1/chat/completions",
	}
	for _, p := range expectedPaths {
		if _, ok := paths[p]; !ok {
			t.Errorf("missing path %s in spec", p)
		}
	}

	components := spec["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	expectedSchemas := []string{"chatRequest", "chatResponse", "errorResponse", "healthResponse"}
	for _, s := range expectedSchemas {
		if _, ok := schemas[s]; !ok {
			t.Errorf("missing schema %s in components", s)
		}
	}
}

func TestOpenAPISpecContentType(t *testing.T) {
	srv := newTestHTTPServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/openapi.json", srv.handleOpenAPISpec)

	req := httptest.NewRequest("GET", "/v1/openapi.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}
}
