package tools

import (
	"errors"
	"testing"
)

func TestIsMCPConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"connection refused", errors.New("dial tcp 127.0.0.1:19094: connection refused"), true},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		{"eof", errors.New("unexpected EOF"), true},
		{"timeout", errors.New("context deadline exceeded"), true},
		{"closed session", errors.New("use of closed network connection"), true},
		{"transport error", errors.New("transport: connection closed"), true},
		{"windows wsarecv", errors.New("wsarecv: An existing connection was forcibly closed"), true},
		{"normal tool error", errors.New("knowledge base not found: abc"), false},
		{"invalid params", errors.New("missing required parameter: query"), false},
		{"server 500", errors.New("HTTP 500: internal server error"), false},
		{"auth error", errors.New("HTTP 401: unauthorized"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isMCPConnectionError(tt.err)
			if got != tt.expected {
				t.Errorf("isMCPConnectionError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}
