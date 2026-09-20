package bench

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

func num(v *float64, format string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf(format, *v)
}

var metricHeaders = []string{"BACKEND", "MODEL", "RUNS", "FAIL", "P50", "P95", "WALL", "COST", "ACC", "MAE", "BRIER", "SELF-SD", "ORDER", "GAP-RECALL"}

func (m Metrics) cells() []string {
	return []string{m.Backend, m.Model, fmt.Sprint(m.Runs), fmt.Sprint(m.Failed),
		fmt.Sprintf("%.0fms", m.LatencyP50MS), fmt.Sprintf("%.0fms", m.LatencyP95MS),
		(time.Duration(m.WallMS) * time.Millisecond).Round(100 * time.Millisecond).String(), fmt.Sprintf("$%.4f", m.CostUSD),
		num(m.Accuracy, "%.2f"), num(m.ScoreMAE, "%.2f"), num(m.Brier, "%.3f"), num(m.SelfConsistency, "%.3f"), num(m.OrderBias, "%.3f"), num(m.GapRecall, "%.2f")}
}

// Table renders the report for a terminal.
func (r Report) Table() string {
	t := table.New().Border(lipgloss.RoundedBorder()).Headers(metricHeaders...)
	for _, m := range r.Backends {
		t.Row(m.cells()...)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d ideas × %d repeats · %s\n%s\n", r.Ideas, r.Repeats, r.Dataset, t.Render())
	b.WriteString("ACC: choice+noul agreement with labels · MAE: score error in levels · BRIER: lower is better\nSELF-SD: std-dev of composite across repeats · ORDER: mean |Δp| between shuffled runs\n")
	for _, a := range r.Agreement {
		fmt.Fprintf(&b, "%s vs %s (%d ideas): kappa %.2f · spearman %.2f\n", a.A, a.B, a.Ideas, a.Kappa, a.Spearman)
	}
	return b.String()
}

// Write saves <dir>/<timestamp>.json and .csv, returning the JSON path.
func (r Report) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(dir, strings.NewReplacer(":", "", "-", "").Replace(r.CreatedAt))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(base+".json", b, 0o644); err != nil {
		return "", err
	}
	f, err := os.Create(base + ".csv")
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"backend", "idea_id", "repeat", "status", "verdict", "composite", "confidence", "idea_type", "rubric", "total_ms", "cost_usd", "missing", "error"})
	for _, run := range r.Runs {
		_ = w.Write(run.csv())
	}
	w.Flush()
	return base + ".json", w.Error()
}

func (run Run) csv() []string {
	row := []string{run.Backend, run.IdeaID, fmt.Sprint(run.Repeat)}
	res := run.Result
	if res == nil {
		return append(row, "error", "", "", "", "", "", "", "", "", run.Err)
	}
	ideaType, rubric := "", ""
	if res.IdeaType != nil {
		ideaType = res.IdeaType.Choice
	}
	if res.Rubric != nil {
		rubric = res.Rubric.Name
	}
	missing := make([]string, len(res.Missing))
	for i, m := range res.Missing {
		missing[i] = m.ID
	}
	return append(row, res.Status, res.Verdict, fmt.Sprintf("%.4f", res.Composite), fmt.Sprintf("%.4f", res.CompositeConfidence),
		ideaType, rubric, fmt.Sprint(res.Timing.TotalMS), fmt.Sprintf("%.6f", res.CostEstimateUSD), strings.Join(missing, " "), res.Error)
}

// ReadReport loads a saved result file.
func ReadReport(path string) (Report, error) {
	var r Report
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(b, &r)
}

// Compare diffs two runs backend by backend (rubric edits, model upgrades).
func Compare(before, after Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "before: %s (%s)\nafter:  %s (%s)\n", before.CreatedAt, before.Dataset, after.CreatedAt, after.Dataset)
	old := map[string]Metrics{}
	for _, m := range before.Backends {
		old[m.Backend] = m
	}
	t := table.New().Border(lipgloss.RoundedBorder()).Headers("BACKEND", "METRIC", "BEFORE", "AFTER", "Δ")
	for _, m := range after.Backends {
		o, ok := old[m.Backend]
		if !ok {
			t.Row(m.Backend, "(new backend)", "", "", "")
			continue
		}
		p50a, p50b := o.LatencyP50MS, m.LatencyP50MS
		for _, d := range []struct {
			name string
			a, b *float64
		}{{"accuracy", o.Accuracy, m.Accuracy}, {"score_mae", o.ScoreMAE, m.ScoreMAE}, {"brier", o.Brier, m.Brier},
			{"self_consistency", o.SelfConsistency, m.SelfConsistency}, {"gap_recall", o.GapRecall, m.GapRecall},
			{"latency_p50_ms", &p50a, &p50b}, {"cost_usd", &o.CostUSD, &m.CostUSD}} {
			delta := "—"
			if d.a != nil && d.b != nil {
				delta = fmt.Sprintf("%+.4f", *d.b-*d.a)
			}
			t.Row(m.Backend, d.name, num(d.a, "%.4f"), num(d.b, "%.4f"), delta)
		}
	}
	b.WriteString(t.Render() + "\n")
	return b.String()
}
