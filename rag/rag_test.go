package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mockProvider struct {
	name string
	docs []Document
	err  error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Query(_ context.Context, _ string, _ int) ([]Document, error) {
	return m.docs, m.err
}

func TestManagerBuildContext_Empty(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	result := mgr.BuildContext(context.Background(), "test query")
	if result != "" {
		t.Errorf("expected empty result with no providers, got: %s", result)
	}
}

func TestManagerBuildContext_EmptyQuery(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.AddProvider(&mockProvider{name: "test", docs: []Document{{Content: "doc"}}})
	result := mgr.BuildContext(context.Background(), "")
	if result != "" {
		t.Errorf("expected empty result with empty query, got: %s", result)
	}
}

func TestManagerBuildContext_WithDocs(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.AddProvider(&mockProvider{
		name: "KB1",
		docs: []Document{
			{Content: "Go is a statically typed language", Source: "go-intro.md", Score: 0.95},
			{Content: "Go was designed at Google", Source: "go-history.md", Score: 0.80},
		},
	})

	result := mgr.BuildContext(context.Background(), "what is Go?")

	if result == "" {
		t.Fatal("expected non-empty RAG context")
	}
	if !contains(result, "<rag-context>") {
		t.Error("expected <rag-context> fence")
	}
	if !contains(result, "KB1") {
		t.Error("expected provider name in output")
	}
	if !contains(result, "Go is a statically typed") {
		t.Error("expected document content in output")
	}
	if !contains(result, "go-intro.md") {
		t.Error("expected source in output")
	}
}

func TestManagerHasProviders(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	if mgr.HasProviders() {
		t.Error("expected no providers initially")
	}
	mgr.AddProvider(&mockProvider{name: "test"})
	if !mgr.HasProviders() {
		t.Error("expected to have providers after add")
	}
}

func TestHTTPProvider(t *testing.T) {
	// Create mock RAG API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"documents": []map[string]interface{}{
				{
					"content": "Retrieved document about Go",
					"source":  "test.md",
					"score":   0.9,
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider, err := NewHTTPProvider(HTTPProviderConfig{
		Name:    "Test RAG",
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	if provider.Name() != "Test RAG" {
		t.Errorf("expected name 'Test RAG', got %s", provider.Name())
	}

	docs, err := provider.Query(context.Background(), "Go language", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if docs[0].Content != "Retrieved document about Go" {
		t.Errorf("unexpected content: %s", docs[0].Content)
	}
	if docs[0].Score != 0.9 {
		t.Errorf("unexpected score: %f", docs[0].Score)
	}
}

func TestHTTPProvider_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	provider, _ := NewHTTPProvider(HTTPProviderConfig{BaseURL: server.URL})
	_, err := provider.Query(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestHTTPProvider_MissingURL(t *testing.T) {
	_, err := NewHTTPProvider(HTTPProviderConfig{})
	if err == nil {
		t.Fatal("expected error for missing base_url")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
