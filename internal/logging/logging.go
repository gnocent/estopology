// Package logging builds the process logger (stdout, text or JSON, SPEC §12.3).
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w. level is debug, info, warn or error;
// format is text or json. Values are assumed validated by config.Load.
func New(w io.Writer, level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
