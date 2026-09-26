package logging

import (
	"bytes"
	"errors"
	"testing"
)

// A line logged through Slog reads exactly as one logged with zerolog.
func TestSlogWritesTheSameLine(t *testing.T) {
	var direct, bridged bytes.Buffer
	err := errors.New("disk full")
	zl := New(&direct, "info", "json", false)
	zl.Warn().Err(err).Str("request_id", "chk_1").Msg("result was not saved to history")
	Slog(New(&bridged, "info", "json", false)).Warn("result was not saved to history", "error", err, "request_id", "chk_1")
	strip := func(b []byte) string { return string(bytes.Split(b, []byte(`"time":`))[0]) }
	if strip(direct.Bytes()) != strip(bridged.Bytes()) || direct.Len() != bridged.Len() {
		t.Errorf("zerolog %s\nslog    %s", direct.String(), bridged.String())
	}
	bridged.Reset()
	Slog(New(&bridged, "warn", "json", false)).Info("quiet")
	if bridged.Len() != 0 {
		t.Errorf("a line below the level was written: %s", bridged.String())
	}
}
