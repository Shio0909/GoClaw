// Package logger provides structured logging for GoClaw using slog.
//
// Call Init() early in main to configure the global logger.
// After Init(), both slog.Info/Warn/Error and legacy log.Printf
// are routed through the same structured handler.
package logger

import (
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
)

// Config controls the logger behavior.
type Config struct {
	Level  string // "debug", "info", "warn", "error" (default: "info")
	Format string // "text" or "json" (default: "text")
	Output io.Writer
}

// Init configures the global slog logger and redirects legacy log.* output.
// It reads GOCLAW_LOG_LEVEL and GOCLAW_LOG_FORMAT from env if Config fields
// are empty.
func Init(cfg Config) *slog.Logger {
	if cfg.Level == "" {
		cfg.Level = os.Getenv("GOCLAW_LOG_LEVEL")
	}
	if cfg.Format == "" {
		cfg.Format = os.Getenv("GOCLAW_LOG_FORMAT")
	}
	if cfg.Output == nil {
		cfg.Output = os.Stderr
	}

	level := parseLevel(cfg.Level)

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Shorten time format for text output
			if a.Key == slog.TimeKey && strings.ToLower(cfg.Format) != "json" {
				a.Value = slog.StringValue(a.Value.Time().Format("15:04:05.000"))
			}
			return a
		},
	}

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(cfg.Output, opts)
	default:
		handler = slog.NewTextHandler(cfg.Output, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Route legacy log.Printf through slog
	log.SetFlags(0)
	log.SetOutput(&slogWriter{logger: logger})

	return logger
}

// parseLevel converts string to slog.Level
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// slogWriter bridges log.Printf output to slog.Info
type slogWriter struct {
	logger *slog.Logger
}

func (w *slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	w.logger.Info(msg)
	return len(p), nil
}
