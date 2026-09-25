package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/morethancoder/ideacheck/ideacheck"
)

const (
	formatPretty = "pretty"
	formatJSON   = "json"
	formatPlain  = "plain"
)

// outputFormat resolves -o / --json / TTY: explicit wins; otherwise JSON whenever
// stdout is not a terminal, so agents never have to ask.
func outputFormat(flag string, jsonFlag, stdoutTTY bool) (string, error) {
	switch {
	case jsonFlag || flag == formatJSON:
		return formatJSON, nil
	case flag == formatPlain || flag == formatPretty:
		return flag, nil
	case flag != "":
		return "", fmt.Errorf("--output %q is not one of pretty, json, plain", flag)
	case stdoutTTY:
		return formatPretty, nil
	}
	return formatJSON, nil
}

func renderJSON(w io.Writer, res *ideacheck.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// renderPlain is the no-bubbletea text view (-o plain).
func renderPlain(w io.Writer, res *ideacheck.Result) error {
	var b strings.Builder
	switch res.Status {
	case ideacheck.StatusNeedsInput:
		fmt.Fprintf(&b, "NEEDS INPUT — answer these before the idea can be judged:\n")
	case ideacheck.StatusError:
		fmt.Fprintf(&b, "ERROR — %s\n", res.Error)
	default:
		fmt.Fprintf(&b, "%s  composite %.2f  confidence %.2f\n%s\n", strings.ToUpper(res.Verdict), res.Composite, res.CompositeConfidence, res.VerdictReason)
	}
	if res.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n\n", res.Summary)
	}
	for _, m := range res.Missing {
		fmt.Fprintf(&b, "  ? %s  (p=%.2f)\n", m.Ask, m.Probability)
	}
	plainList(&b, "Strengths", res.TopStrengths)
	plainList(&b, "Risks", res.TopRisks)
	if r := res.Research; r != nil {
		fmt.Fprintf(&b, "Found on the web (by %s):\n", r.By)
		for _, f := range r.Findings {
			if f.Used {
				fmt.Fprintf(&b, "  %-15s %s  %s\n", f.Topic, f.Title, f.URL)
			}
		}
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", warn)
	}
	if res.Rubric != nil {
		fmt.Fprintf(&b, "rubric %s · ", res.Rubric.Name)
	}
	fmt.Fprintf(&b, "%s/%s (%s) · %d ms · %s\n", res.Backend, res.Model, res.Method, res.Timing.TotalMS, res.ID)
	if res.Cost.Basis != "" { // results saved before costs were recorded have none
		fmt.Fprintf(&b, "cost: %s\n", plainCost(res.Cost))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func plainList(b *strings.Builder, title string, cs []ideacheck.Contribution) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, c := range cs {
		fmt.Fprintf(b, "  %-26s %.2f  (weight %.1f)\n", c.ID, c.Value, c.Weight)
	}
}

func plainCost(c ideacheck.Cost) string {
	switch c.Basis {
	case ideacheck.CostFree, ideacheck.CostUnpriced:
		return c.Basis + " — " + c.Note
	}
	out := fmt.Sprintf("$%.4f (%s) · %d tokens in", c.USD, c.Basis, c.TokensIn)
	if c.TokensCached > 0 {
		out += fmt.Sprintf(" (%d cached)", c.TokensCached)
	}
	out += fmt.Sprintf(" · %d out", c.TokensOut)
	if c.Price != nil {
		out += fmt.Sprintf(" · $%.2f/$%.2f per MTok", c.Price.In, c.Price.Out)
	}
	if c.Note != "" {
		out += " · " + c.Note
	}
	return out
}
