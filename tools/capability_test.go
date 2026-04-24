package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInvokeCapabilityToolStub(t *testing.T) {
	tool := NewInvokeCapabilityTool(CapabilityToolConfig{Stub: true, Timeout: time.Second})
	result, err := tool.Fn(context.Background(), map[string]interface{}{
		"capability": "rag.search",
		"input":      `{"query":"GraphRAG"}`,
	})
	if err != nil {
		t.Fatalf("invoke stub error = %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("stub result is not JSON: %v", err)
	}
	if payload["capability"] != "rag.search" || payload["stub"] != true {
		t.Fatalf("unexpected stub payload: %#v", payload)
	}
}

func TestInvokeCapabilityToolHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var req capabilityRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Capability != "rag.read_wiki" {
			t.Fatalf("capability = %q", req.Capability)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tool := NewInvokeCapabilityTool(CapabilityToolConfig{
		Endpoint: server.URL,
		APIKey:   "test-key",
		Timeout:  time.Second,
	})
	result, err := tool.Fn(context.Background(), map[string]interface{}{
		"capability": "rag.read_wiki",
		"input":      `{"path":"index.md"}`,
	})
	if err != nil {
		t.Fatalf("invoke http error = %v", err)
	}
	if result != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}
