package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/bench"
	"github.com/morethancoder/ideacheck/internal/judge/backends"
	"github.com/morethancoder/ideacheck/internal/judge/backends/mock"
	"github.com/morethancoder/ideacheck/internal/pipeline"
)

const benchResultsDir = "bench/results"

func (a *app) benchCmd() *cobra.Command {
	var backendList, dataset, rubricName string
	var repeats, parallel int
	var compare, dryRun bool
	cmd := &cobra.Command{
		Use: "bench [before.json after.json]", Aliases: []string{"benchmark"},
		Short: "compare backends/models/rubrics on the seed dataset",
		RunE: func(cmd *cobra.Command, args []string) error {
			if compare {
				return a.benchCompare(args)
			}
			if len(args) > 0 {
				return errors.New("bench takes no arguments (did you mean --compare A B?)")
			}
			names := strings.Split(backendList, ",")
			if dryRun {
				names = []string{mock.Name}
			}
			if backendList == "" && !dryRun {
				return errors.New("name the backends to compare, e.g. -b structured,logprob (or --dry-run)")
			}
			runner := bench.Runner{Engines: map[string]bench.Checker{}, Order: names, Repeats: repeats, Parallel: parallel, Rubric: rubricName}
			for _, name := range names {
				cfg, err := a.loadConfig(&checkFlags{backend: strings.TrimSpace(name)})
				if err != nil {
					return err
				}
				judge, err := backends.New(cfg, backends.Deps{Files: a.files(), Secret: a.secrets().Get})
				if err != nil {
					return err
				}
				runner.Engines[name] = &pipeline.Engine{Config: cfg, Files: a.files(), Judge: judge}
			}
			cfg, err := a.loadConfig(&checkFlags{backend: mock.Name})
			if err != nil {
				return err
			}
			catalog, err := bench.Catalog(a.files(), cfg.RubricsDir)
			if err != nil {
				return err
			}
			ideas, err := bench.Load(dataset, catalog)
			if err != nil {
				return err
			}
			p := a.printer(a.stderr)
			p.Title("ideacheck", "bench")
			p.Row("plan", fmt.Sprintf("%d ideas × %d repeats × %d backend(s)", len(ideas), repeats, len(names)))
			p.Pending("running", strings.Join(names, ", "))
			defer p.Stop()
			report := runner.Execute(cmd.Context(), dataset, ideas, catalog, time.Now())
			path, err := report.Write(benchResultsDir)
			if err != nil {
				return err
			}
			p.Row("saved", path+"  (+ .csv)")
			p.Stop() // the table is the output; nothing live may be left over it
			_, err = fmt.Fprintln(a.stdout, report.Table())
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&backendList, "backends", "b", "", "comma-separated backends to compare")        // b = backends, as on the default action
	f.IntVarP(&repeats, "repeats", "n", 3, "runs per idea")                                       // -n = count, as in head -n
	f.StringVarP(&dataset, "dataset", "d", "bench/ideas.jsonl", "ideas file (JSONL)")             // d = dataset
	f.StringVarP(&rubricName, "rubric", "r", "", "force one rubric for every idea")               // r = rubric, as on the default action
	f.IntVar(&parallel, "parallel", 4, "ideas in flight per backend")                             // long only: tuning knob
	f.BoolVar(&compare, "compare", false, "diff two result files: bench --compare A.json B.json") // long only
	f.BoolVar(&dryRun, "dry-run", false, "use the mock backend to test the harness")              // long only, by convention
	return cmd
}

func (a *app) benchCompare(args []string) error {
	if len(args) != 2 {
		return errors.New("--compare needs two result files: bench --compare before.json after.json")
	}
	before, err := bench.ReadReport(args[0])
	if err != nil {
		return err
	}
	after, err := bench.ReadReport(args[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(a.stdout, bench.Compare(before, after))
	return err
}
