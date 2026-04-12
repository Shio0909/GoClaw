package gateway

import (
	"testing"
)

func TestExtractReplyID(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"hello world", ""},
		{"[CQ:reply,id=12345]hello", "12345"},
		{"[CQ:reply,id=-12345]hello", "-12345"},
		{"[CQ:reply,id=99][CQ:at,qq=123]hey", "99"},
	}

	for _, tt := range tests {
		got := extractReplyID(tt.msg)
		if got != tt.want {
			t.Errorf("extractReplyID(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestStripCQReply(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"[CQ:reply,id=12345]hello world", "hello world"},
		{"[CQ:reply,id=99][CQ:at,qq=123] hey", "[CQ:at,qq=123] hey"},
		{"no reply here", "no reply here"},
	}

	for _, tt := range tests {
		got := stripCQReply(tt.msg)
		if got != tt.want {
			t.Errorf("stripCQReply(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestStripAllCQ(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"hello [CQ:at,qq=123] world", "hello  world"},
		{"[CQ:image,url=http://x.com/1.jpg]", ""},
		{"no cq codes", "no cq codes"},
		{"[CQ:face,id=1]笑死", "笑死"},
	}

	for _, tt := range tests {
		got := stripAllCQ(tt.msg)
		if got != tt.want {
			t.Errorf("stripAllCQ(%q) = %q, want %q", tt.msg, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s       string
		max     int
		want    string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"你好世界", 2, "你好..."},
	}

	for _, tt := range tests {
		got := truncate(tt.s, tt.max)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
		}
	}
}

func TestPendingRequests(t *testing.T) {
	pr := newPendingRequests()

	// Register
	echo, ch := pr.register()
	if echo == "" {
		t.Fatal("echo should not be empty")
	}

	// Resolve with matching echo
	resp := apiResponse{Echo: echo, Status: "ok", RetCode: 0}
	ok := pr.resolve(resp)
	if !ok {
		t.Fatal("resolve should return true for matching echo")
	}

	// Should receive on channel
	select {
	case got := <-ch:
		if got.Echo != echo {
			t.Errorf("got echo %q, want %q", got.Echo, echo)
		}
	default:
		t.Fatal("channel should have a response")
	}

	// Resolve with unknown echo
	ok = pr.resolve(apiResponse{Echo: "unknown"})
	if ok {
		t.Fatal("resolve should return false for unknown echo")
	}

	// Resolve with empty echo
	ok = pr.resolve(apiResponse{})
	if ok {
		t.Fatal("resolve should return false for empty echo")
	}
}
