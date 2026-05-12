// Package logger is a tiny wrapper around the standard log/slog package
// configured with the conventions slm-router uses (JSON output to
// stdout, info level by default). Keep this thin — slog is already
// the right shape; we just want a single import path so future
// changes (e.g. adding a request-id correlator) land in one place.
package logger

import (
	"log/slog"
	"os"
)

// New returns a process-wide logger. Call once from main and pass the
// result to subsystems explicitly rather than reaching for a package
// global.
func New(env string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(h).With("service", "slm-router", "env", env)
}
