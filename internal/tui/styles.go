// Package tui is the human mode: intake form → live evaluation → result.
package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/morethancoder/ideacheck/internal/rubric"
)

var (
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	bold    = lipgloss.NewStyle().Bold(true)
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnSty = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)

	verdictColors = map[string]lipgloss.Color{
		rubric.Build: "42", rubric.Explore: "39", rubric.Park: "214", rubric.Kill: "203", rubric.Uncertain: "245",
	}
)

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
