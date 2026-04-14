package logger

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestInitDefault(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Config{Output: &buf})
	l.Info("hello", "key", "val")
	out := buf.String()
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got %q", out)
	}
	if !strings.Contains(out, "key=val") {
		t.Errorf("expected 'key=val' in output, got %q", out)
	}
}

func TestInitJSON(t *testing.T) {
	var buf bytes.Buffer
	l := Init(Config{Format: "json", Output: &buf})
	l.Info("test-msg", "num", 42)
	out := buf.String()
	if !strings.Contains(out, `"msg":"test-msg"`) {
		t.Errorf("expected JSON msg field, got %q", out)
	}
	if !strings.Contains(out, `"num":42`) {
		t.Errorf("expected JSON num field, got %q", out)
	}
}

func TestLogLevel(t *testing.T) {
	var buf bytes.Buffer
	Init(Config{Level: "warn", Output: &buf})
	slog.Info("should-not-appear")
	slog.Warn("should-appear")
	out := buf.String()
	if strings.Contains(out, "should-not-appear") {
		t.Error("info message should be filtered at warn level")
	}
	if !strings.Contains(out, "should-appear") {
		t.Error("warn message should appear at warn level")
	}
}

func TestLegacyLogBridge(t *testing.T) {
	var buf bytes.Buffer
	Init(Config{Output: &buf})
	log.Printf("legacy message %d", 123)
	out := buf.String()
	if !strings.Contains(out, "legacy message 123") {
		t.Errorf("expected legacy log routed through slog, got %q", out)
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}
	for _, tt := range tests {
		got := parseLevel(tt.input)
		if got != tt.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
