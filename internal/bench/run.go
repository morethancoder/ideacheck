package bench

import (
	"context"
	"math"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/morethancoder/ideacheck/internal/judge"
	"github.com/morethancoder/ideacheck/internal/pipeline"
)

// Checker is the slice of pipeline.Engine the runner needs.
type Checker interface {
	Check(ctx context.Context, in pipeline.Intake, o pipeline.Options) (*pipeline.Result, error)
}

type Runner struct {
	Engines  map[string]Checker // backend name → engine
	Order    []string           // backends in the order given
	Repeats  int
	Parallel int    // ideas in flight per backend; questions inside each check fan out on their own
	Rubric   string // force a rubric; "" = route
}

// Run is one (backend, idea, repeat) check.
type Run struct {
	Backend string           `json:"backend"`
	IdeaID  string           `json:"idea_id"`
	Repeat  int              `json:"repeat"`
	Result  *pipeline.Result `json:"result,omitempty"`
	Err     string           `json:"error,omitempty"`
}

type Report struct {
	CreatedAt string      `json:"created_at"`
	Dataset   string      `json:"dataset"`
	Ideas     int         `json:"ideas"`
	Repeats   int         `json:"repeats"`
	Rubric    string      `json:"rubric,omitempty"`
	Backends  []Metrics   `json:"backends"`
	Agreement []Agreement `json:"agreement"`
	Runs      []Run       `json:"runs"`
}

// Execute runs every idea × backend × repeat. Backends run one after another so
// each backend's wall time is its own; ideas within a backend run concurrently.
func (r Runner) Execute(ctx context.Context, dataset string, ideas []Idea, catalog map[string]judge.Question, now time.Time) Report {
	rep := Report{CreatedAt: now.UTC().Format(time.RFC3339), Dataset: dataset, Ideas: len(ideas), Repeats: r.Repeats, Rubric: r.Rubric}
	byBackend := map[string][]Run{}
	for _, backend := range r.Order {
		start := time.Now()
		runs := r.runBackend(ctx, backend, ideas)
		m := Measure(backend, runs, ideas, catalog)
		m.WallMS = time.Since(start).Milliseconds()
		rep.Backends = append(rep.Backends, m)
		rep.Runs = append(rep.Runs, runs...)
		byBackend[backend] = runs
	}
	for i, a := range r.Order {
		for _, b := range r.Order[i+1:] {
			rep.Agreement = append(rep.Agreement, Agree(a, b, byBackend[a], byBackend[b]))
		}
	}
	return rep
}

func (r Runner) runBackend(ctx context.Context, backend string, ideas []Idea) []Run {
	runs := make([]Run, 0, len(ideas)*r.Repeats)
	for _, idea := range ideas {
		for rep := 0; rep < r.Repeats; rep++ {
			runs = append(runs, Run{Backend: backend, IdeaID: idea.ID, Repeat: rep})
		}
	}
	byID := map[string]Idea{}
	for _, idea := range ideas {
		byID[idea.ID] = idea
	}
	var g errgroup.Group
	g.SetLimit(max(r.Parallel, 1))
	for i := range runs {
		g.Go(func() error {
			// Proceed: gaps are measured (recall) but must not stop scoring.
			res, err := r.Engines[backend].Check(ctx, byID[runs[i].IdeaID].Intake(), pipeline.Options{Rubric: r.Rubric, Proceed: true})
			if err != nil {
				runs[i].Err = err.Error()
			}
			runs[i].Result = res
			return nil
		})
	}
	_ = g.Wait()
	return runs
}

// Metrics summarizes one backend. A metric with no data is reported as null.
type Metrics struct {
	Backend         string   `json:"backend"`
	Model           string   `json:"model"`
	Runs            int      `json:"runs"`
	Failed          int      `json:"failed"`
	LatencyP50MS    float64  `json:"latency_p50_ms"`
	LatencyP95MS    float64  `json:"latency_p95_ms"`
	WallMS          int64    `json:"wall_ms"`
	CostUSD         float64  `json:"cost_usd"`
	Accuracy        *float64 `json:"accuracy"`         // choice + noul (threshold 0.5) vs labels
	ScoreMAE        *float64 `json:"score_mae"`        // in levels
	Brier           *float64 `json:"brier"`            // nouls vs 0/1 labels
	SelfConsistency *float64 `json:"self_consistency"` // mean std-dev of composite across repeats
	OrderBias       *float64 `json:"order_bias"`       // logprob: mean |Δp| between shuffled runs
	GapRecall       *float64 `json:"gap_recall"`       // expected missing ids that were produced
}

type tally struct{ sum, n float64 }

func (t *tally) add(v float64) { t.sum += v; t.n++ }
func (t tally) mean() *float64 {
	if t.n == 0 {
		return nil
	}
	m := t.sum / t.n
	return &m
}

// Measure computes a backend's metrics from its runs.
func Measure(backend string, runs []Run, ideas []Idea, catalog map[string]judge.Question) Metrics {
	m := Metrics{Backend: backend, Runs: len(runs)}
	byID := map[string]Idea{}
	for _, idea := range ideas {
		byID[idea.ID] = idea
	}
	var latencies []float64
	var accuracy, mae, brier, bias, recall tally
	composites := map[string][]float64{}
	for _, run := range runs {
		res := run.Result
		if res == nil || res.Status == pipeline.StatusError {
			m.Failed++
			continue
		}
		if m.Model == "" {
			m.Model = res.Model
		}
		latencies = append(latencies, float64(res.Timing.TotalMS))
		m.CostUSD += res.CostEstimateUSD
		if res.Verdict != "" {
			composites[run.IdeaID] = append(composites[run.IdeaID], res.Composite)
		}
		idea := byID[run.IdeaID]
		for _, a := range res.Answers {
			if a.Failed() {
				continue
			}
			if a.OrderBias > 0 || (backend == "logprob" && a.Kind != judge.Noul) {
				bias.add(a.OrderBias)
			}
			scoreLabel(catalog[a.ID], a, idea.Labels[a.ID], &accuracy, &mae, &brier)
		}
		scoreGaps(idea.ExpectMissing, res.Missing, &recall)
	}
	m.LatencyP50MS, m.LatencyP95MS = Percentile(latencies, 50), Percentile(latencies, 95)
	m.Accuracy, m.ScoreMAE, m.Brier, m.OrderBias, m.GapRecall = accuracy.mean(), mae.mean(), brier.mean(), bias.mean(), recall.mean()
	var spread tally
	for _, cs := range composites {
		if len(cs) > 1 {
			spread.add(StdDev(cs))
		}
	}
	m.SelfConsistency = spread.mean()
	return m
}

func scoreLabel(q judge.Question, a judge.Answer, label any, accuracy, mae, brier *tally) {
	if label == nil {
		return
	}
	switch q.Kind {
	case judge.Choice:
		accuracy.add(boolTo(a.Choice == label.(string)))
	case judge.Score:
		mae.add(math.Abs(a.Score - label.(float64)))
	case judge.Noul:
		truth := label.(float64)
		accuracy.add(boolTo((a.Noul >= 0.5) == (truth == 1)))
		brier.add((a.Noul - truth) * (a.Noul - truth))
	}
}

func scoreGaps(expected []string, got []pipeline.Missing, recall *tally) {
	found := map[string]bool{}
	for _, m := range got {
		found[m.ID] = true
	}
	for _, id := range expected {
		recall.add(boolTo(found[id]))
	}
}

func boolTo(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Agreement compares two backends on the ideas both completed.
type Agreement struct {
	A        string  `json:"a"`
	B        string  `json:"b"`
	Ideas    int     `json:"ideas"`
	Kappa    float64 `json:"kappa"`    // Cohen's kappa over idea type and verdict
	Spearman float64 `json:"spearman"` // rank correlation of mean composites
}

type perIdea struct {
	ideaType, verdict string
	composites        []float64
}

func summarize(runs []Run) map[string]*perIdea {
	out := map[string]*perIdea{}
	for _, run := range runs {
		res := run.Result
		if res == nil || res.Verdict == "" {
			continue
		}
		p := out[run.IdeaID]
		if p == nil { // categorical answers come from the first repeat
			p = &perIdea{verdict: res.Verdict}
			if res.IdeaType != nil {
				p.ideaType = res.IdeaType.Choice
			}
			out[run.IdeaID] = p
		}
		p.composites = append(p.composites, res.Composite)
	}
	return out
}

func Agree(a, b string, runsA, runsB []Run) Agreement {
	sa, sb := summarize(runsA), summarize(runsB)
	var ids []string
	for id := range sa {
		if sb[id] != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var la, lb []string
	var ca, cb []float64
	for _, id := range ids {
		la = append(la, sa[id].ideaType, sa[id].verdict)
		lb = append(lb, sb[id].ideaType, sb[id].verdict)
		ca, cb = append(ca, Mean(sa[id].composites)), append(cb, Mean(sb[id].composites))
	}
	return Agreement{A: a, B: b, Ideas: len(ids), Kappa: CohenKappa(la, lb), Spearman: Spearman(ca, cb)}
}
