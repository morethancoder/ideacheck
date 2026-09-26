// Package logging configures zerolog. Logs always go to stderr so stdout stays
// clean JSON for agents.
package logging

import (
	"context"
	"io"
	"log/slog"
	"slices"

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

// Slog is l for packages that log through log/slog (the server, the judges),
// so their lines look like every other line.
func Slog(l zerolog.Logger) *slog.Logger { return slog.New(bridge{l: l}) }

type bridge struct {
	l     zerolog.Logger
	attrs []slog.Attr
}

func (b bridge) Enabled(_ context.Context, lvl slog.Level) bool { return level(lvl) >= b.l.GetLevel() }

func (b bridge) Handle(_ context.Context, r slog.Record) error {
	ev := b.l.WithLevel(level(r.Level))
	add := func(a slog.Attr) bool {
		v := a.Value.Resolve()
		if err, ok := v.Any().(error); ok {
			ev = ev.AnErr(a.Key, err)
		} else if v.Kind() == slog.KindString {
			ev = ev.Str(a.Key, v.String())
		} else {
			ev = ev.Interface(a.Key, v.Any())
		}
		return true
	}
	for _, a := range b.attrs {
		add(a)
	}
	r.Attrs(add)
	ev.Msg(r.Message)
	return nil
}

func (b bridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	b.attrs = append(slices.Clip(b.attrs), attrs...)
	return b
}

// WithGroup flattens: nothing here logs groups.
func (b bridge) WithGroup(string) slog.Handler { return b }

func level(l slog.Level) zerolog.Level {
	switch {
	case l >= slog.LevelError:
		return zerolog.ErrorLevel
	case l >= slog.LevelWarn:
		return zerolog.WarnLevel
	case l >= slog.LevelInfo:
		return zerolog.InfoLevel
	case l >= slog.LevelDebug:
		return zerolog.DebugLevel
	}
	return zerolog.TraceLevel
}
