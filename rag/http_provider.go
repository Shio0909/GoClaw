package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPProvider connects to an external RAG service via HTTP API.
// The external service should accept POST requests with a JSON body:
//
//	{ "query": "...", "max_results": 5 }
//
// And respond with:
//
//	{ "documents": [{ "content": "...", "source": "...", "score": 0.95, "metadata": {} }] }
type HTTPProvider struct {
	name    string
	baseURL string       // e.g., "http://localhost:8000/api/search"
	apiKey  string       // optional Bearer token
	client  *http.Client
}

// HTTPProviderConfig configures the HTTP RAG provider
type HTTPProviderConfig struct {
	Name    string // Display name (default: "HTTP RAG")
	BaseURL string // Required: endpoint URL
	APIKey  string // Optional: Bearer auth token
	Timeout time.Duration
}

// NewHTTPProvider creates an HTTP-based RAG provider
func NewHTTPProvider(cfg HTTPProviderConfig) (*HTTPProvider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("base_url is required for HTTP RAG provider")
	}
	name := cfg.Name
	if name == "" {
		name = "HTTP RAG"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &HTTPProvider{
		name:    name,
		baseURL: cfg.BaseURL,
		apiKey:  cfg.APIKey,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

func (p *HTTPProvider) Name() string { return p.name }

// Query sends a search request to the external RAG API
func (p *HTTPProvider) Query(ctx context.Context, query string, maxResults int) ([]Document, error) {
	reqBody := map[string]interface{}{
		"query":       query,
		"max_results": maxResults,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Documents []struct {
			Content  string            `json:"content"`
			Source   string            `json:"source"`
			Score    float64           `json:"score"`
			Metadata map[string]string `json:"metadata"`
		} `json:"documents"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	docs := make([]Document, len(result.Documents))
	for i, d := range result.Documents {
		docs[i] = Document{
			Content:  d.Content,
			Source:   d.Source,
			Score:    d.Score,
			Metadata: d.Metadata,
		}
	}
	return docs, nil
}
