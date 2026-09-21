package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/morethancoder/ideacheck/internal/pipeline"
)

// CalibrationNote says how far to trust the probabilities, per answer method.
func CalibrationNote(method string) string {
	switch {
	case strings.HasPrefix(method, "vote:k="):
		return "empirical from " + strings.TrimPrefix(method, "vote:k=") + " samples"
	case method == "logprob":
		return "raw logits, not calibrated"
	case method == "jev":
		return "vendor-calibrated"
	case method == "verbalized":
		return "model-stated probabilities, weakly calibrated"
	case method == "mock":
		return "mock values, not a judgment"
	}
	return "calibration unknown"
}

// RenderResult is the result screen. It is a plain string so `last` and `show`
// can reuse it without a running program, and it carries the same left margin
// as every other thing ideacheck prints, so a result left in the scrollback
// lines up with the steps above it.
func RenderResult(res *pipeline.Result) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(result(res), "\n"), "\n") {
		if line = strings.TrimRight(line, " "); line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func result(res *pipeline.Result) string {
	var b strings.Builder
	b.WriteString(headline(res) + "\n")
	if res.Summary != "" {
		b.WriteString("\n" + summaryView(res.Summary, 90) + "\n")
	}
	if len(res.Missing) > 0 {
		b.WriteString(panel.BorderForeground(lipgloss.Color("3")).Render(missingPanel(res.Missing)) + "\n")
	}
	b.WriteString(contributions("Top strengths", good, "▲", res.TopStrengths))
	b.WriteString(contributions("Top risks", bad, "▼", res.TopRisks))
	if res.Research != nil {
		b.WriteString("\n" + EvidenceView(res.Research))
	}
	for _, w := range res.Warnings {
		b.WriteString(warnSty.Bold(true).Render("! ") + warnSty.Render(w) + "\n")
	}
	if line := costLine(res.Cost); line != "" {
		b.WriteString("\n" + line + "\n")
	}
	b.WriteString(dim.Render(footer(res)) + "\n")
	return b.String()
}

// summaryView wraps the plain-language paragraph to a readable width.
func summaryView(text string, width int) string {
	return lipgloss.NewStyle().Width(min(max(width, 40), 100)).Render(text)
}

// costLine is the one-line cost summary; costDetails shows the working. Both
// are empty for results saved before costs were recorded.
func costLine(c pipeline.Cost) string {
	head := heading.Render("Cost") + "  "
	switch c.Basis {
	case "":
		return ""
	case pipeline.CostFree:
		return head + good.Render("free") + dim.Render(" · "+c.Note)
	case pipeline.CostUnpriced:
		return head + "unknown" + dim.Render(" · "+c.Note)
	}
	line := head + bold.Render(usd(c.USD))
	if c.Model != "" {
		line += dim.Render(" · " + c.Model)
	}
	line += dim.Render(" · " + tokens(c))
	if c.Note != "" {
		line += "\n      " + dim.Render(c.Note)
	}
	return line
}

func costDetails(c pipeline.Cost) string {
	if c.Basis == "" {
		return ""
	}
	rows := [][2]string{{"Total", usd(c.USD)}, {"How", map[string]string{
		pipeline.CostReported: "reported by the provider",
		pipeline.CostPriced:   "tokens × the pricing table in config.yaml",
		pipeline.CostFree:     "free",
		pipeline.CostUnpriced: "not priced",
	}[c.Basis]}}
	if c.Model != "" {
		rows = append(rows, [2]string{"Model", c.Model})
	}
	rows = append(rows, [2]string{"Tokens", tokens(c)})
	if p := c.Price; p != nil {
		rate := fmt.Sprintf("$%.2f in · $%.2f out", p.In, p.Out)
		if p.CachedIn > 0 {
			rate += fmt.Sprintf(" · $%.3f cached in", p.CachedIn)
		}
		rows = append(rows, [2]string{"Price", rate + " per million tokens"})
	}
	if c.Note != "" {
		rows = append(rows, [2]string{"Note", c.Note})
	}
	var b strings.Builder
	b.WriteString(heading.Render("Cost of this check") + "\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s %s\n", dim.Render(fmt.Sprintf("%-7s", r[0])), r[1])
	}
	return b.String()
}

// usd keeps small checks readable: $0.0042, $0.015, $1.20.
func usd(v float64) string {
	switch {
	case v > 0 && v < 0.01:
		return fmt.Sprintf("$%.4f", v)
	case v < 1:
		return fmt.Sprintf("$%.3f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

func tokens(c pipeline.Cost) string {
	in := count(c.TokensIn) + " in"
	if c.TokensCached > 0 {
		in += " (" + count(c.TokensCached) + " cached)"
	}
	return in + " · " + count(c.TokensOut) + " out"
}

// count is a token count at a glance: 950, 12.3k, 1.2M.
func count(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func headline(res *pipeline.Result) string {
	switch res.Status {
	case pipeline.StatusNeedsInput:
		return warnSty.Bold(true).Render("NEEDS INPUT") + "  more information is needed before this idea can be judged"
	case pipeline.StatusError:
		return bad.Bold(true).Render("ERROR") + "  " + res.Error
	}
	color := lipgloss.NewStyle().Bold(true).Foreground(verdictColors[res.Verdict])
	return fmt.Sprintf("%s  %s %s  %s %s\n%s",
		badge(res.Verdict), dim.Render("composite"), color.Render(fmt.Sprintf("%.2f", res.Composite)),
		dim.Render("confidence"), bold.Render(strings.TrimSpace(pct(res.CompositeConfidence))), res.VerdictReason)
}

func missingPanel(missing []pipeline.Missing) string {
	lines := []string{warnSty.Bold(true).Render("Missing information")}
	for _, m := range missing {
		lines = append(lines, fmt.Sprintf("%s %s %s", warnSty.Bold(true).Render("?"), m.Ask, dim.Render(fmt.Sprintf("(p=%.2f)", m.Probability))))
	}
	return strings.Join(lines, "\n")
}

// EvidenceView lists what the web lookup found, each with its source: a verdict
// that leans on a search has to show the search. A finding the judge typed as
// unrelated is shown struck from the evidence rather than hidden.
func EvidenceView(r *pipeline.ResearchReport) string {
	head := heading.Render("Found on the web") + dim.Render("  by "+r.By)
	if n := len(r.Queries); n > 0 {
		head += dim.Render(fmt.Sprintf(" · %d searches", n))
	}
	if r.Cached {
		head += dim.Render(" · reused from an earlier check of this idea")
	}
	if len(r.Findings) == 0 {
		return head + "\n  " + dim.Render("The search found nothing on any topic.") + "\n"
	}
	var b strings.Builder
	b.WriteString(head + "\n")
	topic := ""
	for _, f := range r.Findings {
		if f.Topic != topic {
			topic = f.Topic
			b.WriteString("  " + accent.Render(strings.ReplaceAll(topic, "_", " ")) + "\n")
		}
		mark, title := good.Render("•"), bold.Render(f.Title)
		if !f.Used {
			mark, title = dim.Render("×"), dim.Render(f.Title+" — left out")
		}
		line := fmt.Sprintf("    %s %s", mark, title)
		if f.Relation != "" {
			line += dim.Render("  " + f.Relation)
		}
		b.WriteString(line + "\n")
		if f.Used && f.Summary != "" {
			b.WriteString("      " + f.Summary + "\n")
		}
		if f.URL != "" {
			b.WriteString("      " + accent.Faint(true).Underline(true).Render(f.URL) + "\n")
		}
	}
	return b.String()
}

// contributions lists the dimensions that moved the score most, their title,
// mark and bar in the one color that says which way they moved it.
func contributions(title string, color lipgloss.Style, mark string, cs []pipeline.Contribution) string {
	if len(cs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + color.Bold(true).Render(title) + "\n")
	for _, c := range cs {
		bar := Bar(c.Value)
		filled := strings.TrimRight(bar, "░")
		fmt.Fprintf(&b, "  %s %-28s %s %s  %s\n", color.Render(mark), c.ID, color.Render(filled)+dim.Render(bar[len(filled):]),
			bold.Render(fmt.Sprintf("%.2f", c.Value)), dim.Render(fmt.Sprintf("weight %.1f", c.Weight)))
	}
	return b.String()
}

func footer(res *pipeline.Result) string {
	parts := []string{}
	if res.Rubric != nil {
		parts = append(parts, "rubric "+res.Rubric.Name)
	}
	parts = append(parts, fmt.Sprintf("judge %s/%s", res.Backend, res.Model))
	if res.Writer != "" {
		parts = append(parts, "writer "+res.Writer)
	}
	parts = append(parts,
		fmt.Sprintf("%s: %s", res.Method, CalibrationNote(res.Method)),
		fmt.Sprintf("%d ms", res.Timing.TotalMS),
		res.ID,
	)
	return "\n" + strings.Join(parts, " · ") + "\nStructures thinking and flags gaps; not a success predictor."
}
