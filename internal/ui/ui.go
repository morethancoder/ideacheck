// Package ui draws the surface every ideacheck command shares with install.sh:
// a narrow column of steps, one row each, with a live line while a step is in
// flight and a progress bar while bytes are moving.
//
// The vocabulary is deliberately small — a row, a pending line, a bar, a warning
// — because the point is that `ideacheck upgrade` looks like the installer that
// put it there, and `ideacheck config set` looks like both.
//
// Everything live (the spinner, the bar, redraws) happens only on a terminal.
// A pipe, a CI log or a test gets exactly one plain line per finished step, in
// the same order, which is what makes the output safe to grep.
package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// labelWidth is the column the values line up in. Ten characters fits the
// longest step word any command uses ("download", "version", "install") with a
// space to spare, and matches install.sh so the two outputs sit flush.
const labelWidth = 10

// hintWidth is the column the closing "what to run next" lines line up in.
const hintWidth = 18

// redrawEvery throttles the progress bar. A fast download reports every few
// kilobytes; repainting that often buys nothing and makes the bar flicker.
const redrawEvery = 60 * time.Millisecond

// spinEvery is how fast the pending spinner turns. Slow enough to read, fast
// enough to look alive.
const spinEvery = 90 * time.Millisecond

// Options describe the terminal being written to.
type Options struct {
	TTY    bool                // live redraws, the spinner and the cursor tricks
	Color  bool                // false for --no-color / NO_COLOR
	Width  int                 // terminal columns; 0 means ask COLUMNS, else 80
	Getenv func(string) string // nil means no environment (tests)
}

// Printer writes the step column. It is safe to use from one goroutine; the
// spinner it owns is the only other writer.
type Printer struct {
	w     io.Writer
	tty   bool
	color bool
	width int
	g     glyphs

	mu      sync.Mutex
	live    bool      // a line is showing that must be erased before anything else
	last    time.Time // last bar repaint, for the throttle
	spin    chan struct{}
	spun    sync.WaitGroup
	hidden  bool // the cursor is hidden while a live line is showing
	frame   int
	pending [2]string // label and text of the live line, for the spinner to repaint
}

// glyphs are the two alphabets: one for a UTF-8 terminal, one for everything
// else. Same choice, and the same test, as install.sh.
type glyphs struct {
	ok, err, busy, wait, fill, rest, arrow string
	spinner                                []string
}

var (
	unicode = glyphs{
		ok: "✓", err: "✗", busy: "↓", wait: "·", fill: "━", rest: "─", arrow: "›",
		spinner: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	}
	ascii = glyphs{
		ok: "+", err: "x", busy: ">", wait: ".", fill: "#", rest: "-", arrow: ">",
		spinner: []string{"|", "/", "-", "\\"},
	}
)

// New returns a Printer for w.
func New(w io.Writer, o Options) *Printer {
	get := o.Getenv
	if get == nil {
		get = func(string) string { return "" }
	}
	p := &Printer{w: w, tty: o.TTY, color: o.Color && o.TTY, width: o.Width, g: glyphsFor(get)}
	if p.width <= 0 {
		p.width = 80
		if n, err := strconv.Atoi(get("COLUMNS")); err == nil && n > 0 {
			p.width = n
		}
	}
	return p
}

// glyphsFor picks the alphabet the terminal can actually render, from the same
// locale variables a shell script would read.
func glyphsFor(get func(string) string) glyphs {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := get(name); v != "" {
			if u := strings.ToUpper(v); strings.Contains(u, "UTF-8") || strings.Contains(u, "UTF8") {
				return unicode
			}
			return ascii
		}
	}
	return ascii
}

// Title opens the column: the command's name, and what this run is. It is the
// same line the installer and the app's own header print, so a user who has
// seen one recognizes the other.
func (p *Printer) Title(name, what string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	fmt.Fprintf(p.w, "\n  %s%s\n\n", p.sty(bold, name), p.sty(dim, "  "+p.g.arrow+"  "+what))
}

// Row finishes a step. It replaces whatever live line is showing.
func (p *Printer) Row(label, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s %s %s\n", p.sty(green, p.g.ok), p.sty(dim, pad(label, labelWidth)), value)
}

// Skip records a step that was deliberately not done — a file left alone, a
// stage nothing asked for. Same column, quieter mark, so a run reads at a
// glance as "these happened, that one didn't".
func (p *Printer) Skip(label, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s %s %s\n", p.sty(dim, p.g.wait), p.sty(dim, pad(label, labelWidth)), p.sty(dim, value))
}

// Value prints one label/value pair with no step mark: the shape used for
// listing what is saved rather than what just happened.
func (p *Printer) Value(label, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "    %s %s\n", p.sty(dim, pad(label, hintWidth)), value)
}

// Pending shows a step in flight, with a turning spinner. The next row, bar,
// warning or error overwrites it. Off a terminal it prints nothing at all: the
// row that follows is the record of what happened.
func (p *Printer) Pending(label, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tty {
		return
	}
	p.stopSpin()
	p.pending = [2]string{label, text}
	p.frame = 0
	p.hide()
	p.drawPending()
	stop := make(chan struct{})
	p.spin = stop
	p.spun.Add(1)
	go p.turn(stop)
}

// turn repaints the live line so the spinner moves while the work is blocked.
func (p *Printer) turn(stop chan struct{}) {
	defer p.spun.Done()
	tick := time.NewTicker(spinEvery)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			p.mu.Lock()
			// A row may have landed between the tick and the lock; repainting
			// then would print the finished step's line a second time.
			if p.spin == stop {
				p.frame++
				p.drawPending()
			}
			p.mu.Unlock()
		}
	}
}

func (p *Printer) drawPending() {
	mark := p.g.spinner[p.frame%len(p.g.spinner)]
	p.clear()
	fmt.Fprintf(p.w, "  %s %s %s", p.sty(dim, mark), p.sty(dim, pad(p.pending[0], labelWidth)), p.sty(dim, p.pending[1]))
	p.live = true
}

// Progress draws a step that is moving bytes. total <= 0 means the size is not
// known yet, and only the amount so far is shown. Calls are throttled, except
// the one that completes the transfer, so the bar always ends full.
func (p *Printer) Progress(label string, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tty {
		return
	}
	p.stopSpin()
	now := time.Now()
	if (total <= 0 || done < total) && now.Sub(p.last) < redrawEvery {
		return // never throttle away the update that fills the bar
	}
	p.last = now
	p.hide()
	p.clear()
	head := fmt.Sprintf("  %s %s", p.sty(dim, p.g.busy), p.sty(dim, pad(label, labelWidth)))
	p.live = true
	if total <= 0 {
		fmt.Fprintf(p.w, "%s %s", head, Bytes(done))
		return
	}
	pct := int(float64(done) / float64(total) * 100)
	pct = min(max(pct, 0), 100)
	width := 24
	if p.width < 66 {
		width = 10
	}
	filled := min(pct*width/100, width)
	bar := p.sty(green, strings.Repeat(p.g.fill, filled)) + p.sty(dim, strings.Repeat(p.g.rest, width-filled))
	fmt.Fprintf(p.w, "%s %s %3d%%  %s", head, bar, pct, p.sty(dim, Bytes(done)+" / "+Bytes(total)))
}

// Warn is something the user should know that did not stop the run.
func (p *Printer) Warn(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s %s\n", p.sty(yellow, "!"), fmt.Sprintf(format, args...))
}

// Fail is the last thing a failed run prints. Later lines of a multi-line
// message are indented under the first, so the column still reads as a column.
func (p *Printer) Fail(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	fmt.Fprintf(p.w, "  %s %s\n", p.sty(red, p.g.err), lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(p.w, "    %s\n", p.sty(dim, line))
	}
	fmt.Fprintln(p.w)
}

// Done closes the column with the sentence that says what now exists.
func (p *Printer) Done(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "\n  %s\n\n", p.sty(bold, fmt.Sprintf(format, args...)))
}

// Hint is one of the closing "here is what to run next" lines.
func (p *Printer) Hint(command, what string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s %s\n", pad(command, hintWidth), p.sty(dim, what))
}

// Head is a section heading inside the column.
func (p *Printer) Head(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s\n", p.sty(bold, fmt.Sprintf(format, args...)))
}

// Line is one plain line in the column, for a listing a row would flatten.
func (p *Printer) Line(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s\n", fmt.Sprintf(format, args...))
}

// Wrap prints dim text folded to the terminal's width and indented under
// whatever it explains, so a long rubric instruction never wraps ragged into
// the left margin.
func (p *Printer) Wrap(indent int, text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	lead := strings.Repeat(" ", 2+indent)
	for _, line := range fold(text, max(p.width-len(lead)-1, 20)) {
		fmt.Fprintf(p.w, "%s%s\n", lead, p.sty(dim, line))
	}
}

// fold breaks text into lines of at most width runes, on spaces. A word longer
// than the width gets its own line rather than being cut in half.
func fold(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Note is a quiet sentence in the column, for what a row cannot hold. Wrap is
// the same thing for text long enough to need folding.
func (p *Printer) Note(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintf(p.w, "  %s\n", p.sty(dim, fmt.Sprintf(format, args...)))
}

// Blank is one empty line, used to group rows.
func (p *Printer) Blank() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	fmt.Fprintln(p.w)
}

// Stop erases anything live and gives the cursor back. Every command that uses
// a Printer defers it, including the ones that end in an error.
func (p *Printer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSpin()
	p.clear()
	p.show()
}

// stopSpin ends the spinner goroutine and waits for it, so no repaint can land
// after the line it belongs to has been replaced. Called with the lock held;
// the goroutine takes the same lock, so it is released around the wait.
func (p *Printer) stopSpin() {
	if p.spin == nil {
		return
	}
	stop := p.spin
	p.spin = nil
	close(stop)
	p.mu.Unlock()
	p.spun.Wait()
	p.mu.Lock()
}

// clear erases the live line, on a terminal that can be told to.
func (p *Printer) clear() {
	if p.tty && p.live {
		fmt.Fprint(p.w, "\r\033[K")
	}
	p.live = false
}

func (p *Printer) hide() {
	if p.tty && !p.hidden {
		fmt.Fprint(p.w, "\033[?25l")
		p.hidden = true
	}
}

func (p *Printer) show() {
	if p.hidden {
		fmt.Fprint(p.w, "\033[?25h")
		p.hidden = false
	}
}

// Bar returns the two halves of a progress bar filled to fraction of width, in
// the glyphs this terminal can draw. It is exported for the app, which colors
// them itself; the two bars then look identical.
func Bar(fraction float64, width int) (filled, rest string) {
	g := envGlyphs()
	n := min(max(int(fraction*float64(width)+0.5), 0), width)
	return strings.Repeat(g.fill, n), strings.Repeat(g.rest, width-n)
}

// envGlyphs is the alphabet for this process, read once from the environment.
var envGlyphs = sync.OnceValue(func() glyphs { return glyphsFor(os.Getenv) })

// Bytes prints a size the way a person reads one during a download.
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n/1024)
}

// pad left-aligns s in a column, without cutting a longer value short: a step
// word that does not fit pushes its value right rather than being truncated.
func pad(s string, width int) string {
	if n := len([]rune(s)); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// The escape codes install.sh uses, so the two agree on what dim means.
const (
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
	reset  = "\033[0m"
)

func (p *Printer) sty(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return code + s + reset
}
