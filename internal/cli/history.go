package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/tui"
	"github.com/morethancoder/ideacheck/store"
)

// openStore opens the history database named by the effective config.
func (a *app) openStore() (*store.Store, error) {
	cfg, err := config.Load(a.files(), config.LoadOptions{Environ: a.environ})
	if err != nil {
		return nil, err
	}
	return store.Open(store.ExpandHome(cfg.Store.Path, a.home))
}

// persist saves a finished check. History is a convenience: failing to write it
// must never cost the user the result they just waited (and paid) for.
func (a *app) persist(ctx context.Context, path string, in ideacheck.Intake, res *ideacheck.Result) error {
	s, err := store.Open(store.ExpandHome(path, a.home))
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Save(ctx, in, res)
}

// show renders a stored result the same way a fresh check would be rendered.
func (a *app) show(res *ideacheck.Result, output string, jsonFlag bool) error {
	format, err := outputFormat(output, jsonFlag, a.stdoutTTY)
	if err != nil {
		return err
	}
	switch format {
	case formatJSON:
		return renderJSON(a.stdout, res)
	case formatPlain:
		return renderPlain(a.stdout, res)
	}
	_, err = fmt.Fprint(a.stdout, tui.RenderResult(res))
	return err
}

func (a *app) stored(use, short string, args cobra.PositionalArgs, find func(context.Context, *store.Store, []string) (*ideacheck.Result, error)) *cobra.Command {
	var output string
	var jsonFlag bool
	cmd := &cobra.Command{
		Use: use, Short: short, Args: args,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			res, err := find(cmd.Context(), s, args)
			if err != nil {
				return err
			}
			return a.show(res, output, jsonFlag)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "pretty, json, plain") // same meaning as on the default action
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "alias for -o json")
	return cmd
}

func (a *app) lastCmd() *cobra.Command {
	return a.stored("last", "re-show the most recent result", cobra.NoArgs,
		func(ctx context.Context, s *store.Store, _ []string) (*ideacheck.Result, error) { return s.Last(ctx) })
}

func (a *app) showCmd() *cobra.Command {
	return a.stored("show <id>", "re-show one past result (history number or chk_ id)", cobra.ExactArgs(1),
		func(ctx context.Context, s *store.Store, args []string) (*ideacheck.Result, error) {
			res, err := s.Get(ctx, args[0])
			if err != nil {
				return nil, fmt.Errorf("%w: %s (see `ideacheck history`)", err, args[0])
			}
			return res, nil
		})
}

func (a *app) historyCmd() *cobra.Command {
	var limit int
	var jsonFlag, plain bool
	cmd := &cobra.Command{
		Use: "history", Aliases: []string{"log", "past"}, Short: "browse past checks in a table", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.openStore()
			if err != nil {
				return err
			}
			defer s.Close()
			rows, err := s.List(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if jsonFlag || !a.stdoutTTY {
				enc := json.NewEncoder(a.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if plain {
				_, err = fmt.Fprintln(a.stdout, historyTable(rows))
				return err
			}
			s.Close() // the app opens its own handle
			return a.openApp(cmd.Context(), &checkFlags{}, tui.Start{Page: tui.OpenHistory, ExitAfter: true})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "rows to show") // -n = count, as in head -n
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print rows as JSON")
	cmd.Flags().BoolVar(&plain, "plain", false, "print a static table instead of opening the browser") // long only
	return cmd
}

func historyTable(rows []store.Row) string {
	if len(rows) == 0 {
		return "No checks yet. Try: ideacheck \"your idea\""
	}
	t := table.New().Border(lipgloss.RoundedBorder()).Headers("#", "WHEN", "VERDICT", "SCORE", "RUBRIC", "BACKEND", "IDEA")
	for _, r := range rows {
		verdict := r.Verdict
		if verdict == "" {
			verdict = r.Status
		}
		t.Row(fmt.Sprint(r.Seq), strings.Replace(strings.TrimSuffix(r.CreatedAt, "Z"), "T", " ", 1), verdict,
			fmt.Sprintf("%.2f", r.Composite), r.Rubric, r.Backend, clip(r.Idea, 48))
	}
	return t.Render() + "\nRe-show one with: ideacheck show <#>"
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
