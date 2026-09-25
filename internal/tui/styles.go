// Package tui is the human mode: intake form → live evaluation → result.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/morethancoder/ideacheck/rubric"
)

// The palette is the install script's — faint text rather than a grey that
// guesses at the theme, and the terminal's own green, red and yellow — plus
// the terminal's own cyan as the accent: what has focus, where you are, which
// key does what. Every color is one of the sixteen the user's theme defines,
// so an ideacheck window looks right in whatever colors they have chosen.
var (
	dim     = lipgloss.NewStyle().Faint(true)
	bold    = lipgloss.NewStyle().Bold(true)
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warnSty = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	heading = accent.Bold(true)
	// pill marks the one thing in a row that is "here": the app's name, the
	// open tab, the menu line under the cursor. It is the accent in reverse
	// video — cyan fill, text in the terminal's own background color — never
	// palette black on cyan: many themes set black near their cyan, and a
	// terminal that draws bold as bright turns it into grey. Not bold either:
	// the padding is drawn without it, and the same terminal then fills the bar
	// in two shades of cyan, brighter under the text than beside it.
	pill  = lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("6")).Padding(0, 1)
	panel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	// callout is a note or an error set off by a colored bar on its left.
	callout = lipgloss.NewStyle().Border(lipgloss.ThickBorder(), false, false, false, true).PaddingLeft(1)

	verdictColors = map[string]lipgloss.Color{
		rubric.Build: "42", rubric.Explore: "39", rubric.Park: "214", rubric.Kill: "203", rubric.Uncertain: "245",
	}
)

// formTheme dresses huh's forms in the same palette as the rest of the tool.
// Its default theme is Charm's indigo and fuchsia, which reads as a different
// program sitting inside this one; these are the app's colors — cyan for the
// question and the line under the cursor, faint for the explanation.
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	f := &t.Focused
	f.Base = f.Base.BorderForeground(lipgloss.Color("6"))
	f.Card = f.Base
	f.Title = f.Title.Bold(true).Foreground(lipgloss.Color("6"))
	f.NoteTitle = f.NoteTitle.Bold(true).Foreground(lipgloss.Color("6")).MarginBottom(1)
	f.Description = f.Description.Faint(true)
	f.ErrorIndicator = f.ErrorIndicator.Foreground(lipgloss.Color("1"))
	f.ErrorMessage = f.ErrorMessage.Foreground(lipgloss.Color("1"))
	f.SelectSelector = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).SetString("› ")
	f.NextIndicator = f.NextIndicator.Foreground(lipgloss.Color("6"))
	f.PrevIndicator = f.PrevIndicator.Foreground(lipgloss.Color("6"))
	f.SelectedOption = f.SelectedOption.Bold(true).Foreground(lipgloss.Color("6"))
	f.SelectedPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).SetString("✓ ")
	f.UnselectedPrefix = lipgloss.NewStyle().Faint(true).SetString("· ")
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(lipgloss.Color("6"))
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(lipgloss.Color("6"))
	f.TextInput.Placeholder = f.TextInput.Placeholder.Faint(true)
	f.TextInput.Text = f.TextInput.Text.Bold(true)
	// Buttons are the pill and its quiet neighbour. The base theme fills the
	// other button with palette black, a dark box on any theme that is not black.
	f.FocusedButton = f.FocusedButton.Reverse(true).Foreground(lipgloss.Color("6")).UnsetBackground()
	f.Next = f.FocusedButton
	f.BlurredButton = f.BlurredButton.Faint(true).UnsetForeground().UnsetBackground()

	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Blurred.Title = t.Focused.Title.UnsetForeground()
	t.Blurred.NextIndicator = lipgloss.NewStyle()
	t.Blurred.PrevIndicator = lipgloss.NewStyle()
	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}

// tableStyles dresses the history table like the menu: quiet column titles and
// the row under the cursor as the same bar. The default is Charm's pink.
func tableStyles() table.Styles {
	cell := lipgloss.NewStyle().Padding(0, 1)
	return table.Styles{Header: cell.Faint(true), Cell: cell, Selected: lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("6"))}
}

// help renders key bindings as "key what": the key in the accent, what it
// does faint, so the eye finds the key first.
func help(width int, pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, accent.Bold(true).Render(pairs[i])+" "+dim.Render(pairs[i+1]))
	}
	return flow(width, dim.Render("  ·  "), parts)
}

// flow joins parts with sep, breaking between parts — never inside one — to
// stay within width.
func flow(width int, sep string, parts []string) string {
	var lines []string
	line := ""
	for _, part := range parts {
		switch {
		case line == "":
			line = part
		case lipgloss.Width(line+sep+part) > width:
			lines, line = append(lines, line), part
		default:
			line += sep + part
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// scoreStyle colors a value by whether it is good news: high is good for a
// question of positive polarity, bad for a negative one; an informational
// question keeps the accent.
func scoreStyle(value float64, polarity int) lipgloss.Style {
	switch {
	case polarity < 0:
		value = 1 - value
	case polarity == 0:
		return accent
	}
	switch {
	case value >= 0.66:
		return good
	case value >= 0.4:
		return warnSty
	}
	return bad
}

// ColorBar is Bar colored by scoreStyle, the empty cells faint.
func ColorBar(value float64, polarity int) string {
	b := Bar(value)
	filled := strings.TrimRight(b, "░")
	return scoreStyle(value, polarity).Render(filled) + dim.Render(b[len(filled):])
}

// DisableColor forces plain output (--no-color; NO_COLOR is honored by termenv itself).
func DisableColor() { lipgloss.SetColorProfile(termenv.Ascii) }

// badge draws the verdict on its own color. The fills are all light, so the
// text is the 256-color cube's black (16), not the palette's 0, which a theme
// may move and a bold-as-bright terminal turns grey.
func badge(verdict string) string {
	return lipgloss.NewStyle().Bold(true).Padding(0, 2).
		Foreground(lipgloss.Color("16")).Background(verdictColors[verdict]).
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
