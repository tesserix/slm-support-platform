package logger

import (
	"log/slog"
	"os"
)

// New returns a slog.Logger configured for either dev (text) or prod (JSON).
func New(env string) *slog.Logger {
	if env == "dev" {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
			// See the JSON handler below.
			AddSource: true,
		}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		// Emits source.{file,line,function} on every record, which is what
		// lets an error in the observability UI be traced back to the exact
		// line that produced it. Paired with -trimpath the path is
		// module-relative and maps onto a file in this repo at the commit the
		// running image was built from.
		AddSource: true,
	}))
}
