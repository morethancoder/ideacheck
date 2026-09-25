package judge

import (
	"context"
	"log/slog"
)

type loggerKey struct{}

// WithLogger returns a context whose backends log to l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// Logger is the context's logger, or one that discards when none was attached.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return discard
}

var discard = slog.New(slog.DiscardHandler)
