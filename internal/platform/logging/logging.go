package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(level, service string) *slog.Logger {
	return newLogger(os.Stdout, level, service)
}

func newLogger(w io.Writer, level, service string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv})
	return slog.New(h).With("service", service)
}
