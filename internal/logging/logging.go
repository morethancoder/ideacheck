// Package logging configures zerolog. Logs always go to stderr so stdout stays
// clean JSON for agents.
package logging

import (
	"context"
	"io"

	"github.com/rs/zerolog"
)

// TraceLevel is where full prompts are logged; API keys are never logged at any level.
const TraceLevel = zerolog.TraceLevel

// New builds the process logger. format is "console" or "json".
func New(w io.Writer, level, format string, color bool) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	if format != "json" {
		w = zerolog.ConsoleWriter{Out: w, NoColor: !color, TimeFormat: "15:04:05"}
	}
	return zerolog.New(w).Level(lvl).With().Timestamp().Logger()
}

// Level maps -q / -v / -vv / -vvv onto a level name; "" keeps the configured one.
func Level(verbose int, quiet bool) string {
	switch {
	case quiet:
		return "error"
	case verbose >= 3:
		return "trace"
	case verbose >= 1:
		return "debug"
	}
	return ""
}

// WithRequestID returns a context whose logger stamps every line with the check id.
func WithRequestID(ctx context.Context, log zerolog.Logger, id string) context.Context {
	return log.With().Str("request_id", id).Logger().WithContext(ctx)
}

// From returns the context's logger (a disabled logger when none was attached).
func From(ctx context.Context) *zerolog.Logger { return zerolog.Ctx(ctx) }
