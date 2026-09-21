package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// chromeHeight is what the app draws around a wizard's form: the padded
// frame, the header and its model line, the step line and the help line.
const chromeHeight = 10

// step is one page of a wizard: a small form of one or two related fields.
type step struct {
	title string
	skip  func() bool // nil = always shown
	build func() *huh.Form
}

// wizard walks steps one page at a time. Enter on the last field advances, esc
// goes back a step (or leaves the wizard from the first one). Field values live
// in variables owned by whoever built the steps, so going back keeps them.
type wizard struct {
	name          string
	steps         []step
	at            int
	form          *huh.Form
	width, height int
	// over, when set, is how many lines the app draws around this wizard beyond
	// chromeHeight: settings' model line wraps in a narrow window.
	over func() int
}

type wizardState int

const (
	wizardRunning wizardState = iota
	wizardDone
	wizardLeft
)

func newWizard(name string, width, height int, steps []step) (*wizard, tea.Cmd) {
	w := &wizard{name: name, steps: steps, at: -1, width: width, height: height}
	_, cmd := w.move(1)
	return w, cmd
}

func (w *wizard) shown(i int) bool { return w.steps[i].skip == nil || !w.steps[i].skip() }

// move steps forward (+1) or back (-1) past skipped steps.
func (w *wizard) move(dir int) (wizardState, tea.Cmd) {
	for i := w.at + dir; ; i += dir {
		switch {
		case i < 0:
			return wizardLeft, nil
		case i >= len(w.steps):
			return wizardDone, nil
		case w.shown(i):
			w.at = i
			form := w.steps[i].build().WithShowHelp(false).WithTheme(formTheme())
			cmd := form.Init() // before sizing: a form renders nothing until it is initialized
			w.form = w.fit(form)
			return wizardRunning, cmd
		}
	}
}

func (w *wizard) update(msg tea.Msg) (wizardState, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		// Not forwarded: the form would size its viewport to the whole window (and
		// to nothing at all on a terminal that reports 0x0). Size is set per step.
		if size.Width > 0 && size.Height > 0 {
			w.width, w.height = size.Width, size.Height
			w.form = w.fit(w.form)
		}
		return wizardRunning, nil
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		return w.move(-1)
	}
	m, cmd := w.form.Update(msg)
	if f, ok := m.(*huh.Form); ok {
		w.form = f
	}
	if w.form.State == huh.StateCompleted {
		return w.move(1)
	}
	return wizardRunning, cmd
}

// fit sizes a form to the terminal. The height must be set explicitly: huh
// sizes a group's viewport when the group is built, at its own default width,
// so a description that wraps to one more line at the real width pushes the
// field itself out of view — a step you can read but not type into. The form
// is then shrunk to its content, which the first sizing padded out to the
// full height; a form taller than the space left keeps it and scrolls.
//
// Each resize is followed by an update that carries nothing: huh draws what
// it laid out on its last update, so without one the text keeps the old
// width — a description on one long line, cut off at the window's edge.
func (w *wizard) fit(form *huh.Form) *huh.Form {
	room := max(w.height-chromeHeight-w.overHeight(), 6)
	form = form.WithWidth(formWidth(w.width)).WithHeight(room)
	form.Update(relayout{})
	if h := lipgloss.Height(strings.TrimRight(form.View(), " \n")); h < room {
		form = form.WithHeight(h)
		form.Update(relayout{})
	}
	return form
}

// relayout is a message no field acts on; it only makes a form lay itself out.
type relayout struct{}

// formWidth is the width forms and the text beside them wrap to: the window
// less the app's padding, never wider than reads comfortably.
func formWidth(window int) int { return min(max(window-4, 20), 90) }

func (w *wizard) overHeight() int {
	if w.over == nil {
		return 0
	}
	return w.over()
}

// position is "2 of 4" counting only the steps that are shown.
func (w *wizard) position() (int, int) {
	at, total := 0, 0
	for i := range w.steps {
		if w.shown(i) {
			total++
			if i <= w.at {
				at++
			}
		}
	}
	return at, total
}

// view heads the form with where the user is: done steps green, this one in
// the accent, the rest faint.
func (w *wizard) view() string {
	at, total := w.position()
	marks := make([]string, total)
	for i := range marks {
		switch {
		case i+1 < at:
			marks[i] = good.Render("●")
		case i+1 == at:
			marks[i] = accent.Render("●")
		default:
			marks[i] = dim.Render("○")
		}
	}
	head := heading.Render(fmt.Sprintf("Step %d of %d", at, total)) + dim.Render(" · ") + bold.Render(w.steps[w.at].title) + "  " + strings.Join(marks, " ")
	return head + "\n\n" + w.form.View()
}
