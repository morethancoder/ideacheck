// Package tui is the human mode: intake form → live evaluation → result.
package tui

import (
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/morethancoder/ideacheck/internal/rubric"
)

// The palette is the install script's: faint text rather than a grey that
// guesses at the theme, and the terminal's own green, red and yellow. An
// ideacheck window then looks like the installer that put it there, in whatever
// colors the user has chosen for their terminal.
var (
	dim     = lipgloss.NewStyle().Faint(true)
	bold    = lipgloss.NewStyle().Bold(true)
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warnSty = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)

	verdictColors = map[string]lipgloss.Color{
		rubric.Build: "42", rubric.Explore: "39", rubric.Park: "214", rubric.Kill: "203", rubric.Uncertain: "245",
	}
)

// formTheme dresses huh's forms in the same palette as the rest of the tool.
// Its default theme is Charm's indigo and fuchsia, which reads as a different
// program sitting inside this one; these are the installer's colors — the
// terminal's own green for what is chosen or being typed, faint for the
// explanation, bold for the question.
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	f := &t.Focused
	f.Base = f.Base.BorderForeground(lipgloss.Color("8"))
	f.Card = f.Base
	f.Title = f.Title.Bold(true)
	f.NoteTitle = f.NoteTitle.Bold(true).MarginBottom(1)
	f.Description = f.Description.Faint(true)
	f.ErrorIndicator = f.ErrorIndicator.Foreground(lipgloss.Color("1"))
	f.ErrorMessage = f.ErrorMessage.Foreground(lipgloss.Color("1"))
	f.SelectSelector = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).SetString("› ")
	f.NextIndicator = f.NextIndicator.Foreground(lipgloss.Color("2"))
	f.PrevIndicator = f.PrevIndicator.Foreground(lipgloss.Color("2"))
	f.SelectedOption = f.SelectedOption.Foreground(lipgloss.Color("2"))
	f.SelectedPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).SetString("✓ ")
	f.UnselectedPrefix = lipgloss.NewStyle().Faint(true).SetString("· ")
	f.MultiSelectSelector = f.SelectSelector
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(lipgloss.Color("2"))
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(lipgloss.Color("2"))
	f.TextInput.Placeholder = f.TextInput.Placeholder.Faint(true)
	f.FocusedButton = f.FocusedButton.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("2"))
	f.Next = f.FocusedButton
	f.BlurredButton = f.BlurredButton.Faint(true)

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}

// DisableColor forces plain output (--no-color; NO_COLOR is honored by termenv itself).
func DisableColor() { lipgloss.SetColorProfile(termenv.Ascii) }

func badge(verdict string) string {
	return lipgloss.NewStyle().Bold(true).Padding(0, 2).
		Foreground(lipgloss.Color("231")).Background(verdictColors[verdict]).
		Render(strings.ToUpper(verdict))
}

const barWidth = 12

// Bar draws value in [0,1] as ████░░, rounding to the nearest cell.
func Bar(value float64) string {
	filled := int(value*barWidth + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

// Arrow shows which direction is good for a question.
func Arrow(polarity int) string {
	switch {
	case polarity > 0:
		return "↑"
	case polarity < 0:
		return "↓"
	}
	return "·"
}
