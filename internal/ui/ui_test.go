package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// env is a terminal whose locale says UTF-8, so the tests read the real glyphs.
func env(k string) string {
	if k == "LANG" {
		return "en_US.UTF-8"
	}
	return ""
}

// TestPipedOutputIsPlainLines: a pipe, a CI log and an agent all read this. It
// must carry no escape codes, no spinner and no bar — one line per finished
// step, in order.
func TestPipedOutputIsPlainLines(t *testing.T) {
	var b bytes.Buffer
	p := New(&b, Options{TTY: false, Color: true, Getenv: env})
	p.Title("ideacheck", "upgrade")
	p.Row("current", "0.1.0")
	p.Pending("latest", "asking GitHub")
	p.Progress("download", 512, 1024)
	p.Row("download", Bytes(1024))
	p.Stop()

	got := b.String()
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape codes off a terminal:\n%q", got)
	}
	want := "\n  ideacheck  ›  upgrade\n\n  ✓ current    0.1.0\n  ✓ download   1 KB\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestLiveLinesAreErased: every live line (the spinner, the bar) is erased
// before the next thing is written, or a finished run would be littered with
// half-drawn steps.
func TestLiveLinesAreErased(t *testing.T) {
	var b bytes.Buffer
	p := New(&b, Options{TTY: true, Color: false, Getenv: env, Width: 80})
	p.Pending("latest", "asking GitHub")
	time.Sleep(3 * spinEvery) // let the spinner turn a few frames
	p.Progress("download", 300, 1000)
	p.Row("download", "1 KB")
	p.Stop()

	got := b.String()
	if n := strings.Count(got, "\r\033[K"); n < 3 {
		t.Errorf("want every live line erased, saw %d erases in:\n%q", n, got)
	}
	if !strings.HasSuffix(got, "  ✓ download   1 KB\n\033[?25h") {
		t.Errorf("a run must end on its last row with the cursor back:\n%q", got)
	}
	if !strings.Contains(got, "━━━") || !strings.Contains(got, " 30%") {
		t.Errorf("want a bar at 30%%:\n%q", got)
	}
}

// TestProgressAlwaysDrawsTheLastByte: the throttle must never swallow the
// update that completes a transfer, or the bar would stop short of full.
func TestProgressAlwaysDrawsTheLastByte(t *testing.T) {
	var b bytes.Buffer
	p := New(&b, Options{TTY: true, Color: false, Getenv: env, Width: 80})
	for done := int64(100); done <= 1000; done += 100 {
		p.Progress("download", done, 1000)
	}
	p.Stop()
	if !strings.Contains(b.String(), "100%") {
		t.Errorf("the completing update was throttled away:\n%q", b.String())
	}
}

func TestBytes(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0 KB"}, {2048, "2 KB"}, {3 << 20, "3.0 MB"}, {2 << 30, "2.0 GB"}} {
		if got := Bytes(c.n); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestASCIITerminalGetsASCII: a terminal that cannot say it speaks UTF-8 gets
// the plain alphabet rather than boxes where the checkmarks should be.
func TestASCIITerminalGetsASCII(t *testing.T) {
	var b bytes.Buffer
	p := New(&b, Options{Getenv: func(k string) string { return map[string]string{"LANG": "C"}[k] }})
	p.Row("current", "0.1.0")
	p.Stop()
	if !strings.HasPrefix(b.String(), "  + current") {
		t.Errorf("got %q", b.String())
	}
}
