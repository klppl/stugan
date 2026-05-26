// Package logging builds the process-wide structured logger from config.
package logging

import (
	"log/slog"
	"os"
)

// New returns an slog.Logger configured from the given level and format.
// level is one of "debug"/"info"/"warn"/"error"; format is "text"/"json".
// Unknown values fall back to info/text. Output goes to stderr.
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
