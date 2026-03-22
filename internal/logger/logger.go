// Package logger provides structured logging via log/slog.
//
// Usage:
//
//	log := logger.New("recorder")
//	log.Info("polling", "feed", "vehicles", "interval", "15s")
//	log.Error("fetch failed", "err", err)
package logger

import (
	"log/slog"
	"os"
)

// Logger wraps slog.Logger with a component name.
type Logger struct {
	*slog.Logger
}

var defaultHandler slog.Handler

func init() {
	defaultHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(defaultHandler))
}

// New creates a logger tagged with a component name.
func New(component string) *Logger {
	return &Logger{
		Logger: slog.New(defaultHandler).With("component", component),
	}
}
