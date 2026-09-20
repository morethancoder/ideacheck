package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/judge/backends"
	"github.com/morethancoder/ideacheck/internal/logging"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/tui"
)

type checkFlags struct {
	output      string
	json        bool
	backend     string
	model       string
	rubric      string
	profile     string
	file        string
	interactive bool
	ask, noAsk  bool
	explain     bool
	noExplain   bool
	timeout     time.Duration
	sequential  bool
}

func (c *checkFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&c.output, "output", "o", "", "pretty (default on a terminal), json, plain")                      // -o = output format (kubectl, gcc, curl)
	f.BoolVar(&c.json, "json", false, "alias for -o json")                                                         // no -j: that means jobs in make/cargo/ninja
	f.StringVarP(&c.backend, "backend", "b", "", "jev, logprob, structured, claude-cli, mock")                     // b = backend
	f.StringVarP(&c.model, "model", "m", "", "override the backend's model")                                       // -m = model in llm, ollama
	f.StringVarP(&c.rubric, "rubric", "r", "", "force a rubric instead of auto-routing")                           // r = rubric
	f.StringVarP(&c.profile, "profile", "P", "", "use a named or alternate profile file")                          // -P: lowercase -p is port/print nearly everywhere
	f.StringVarP(&c.file, "file", "f", "", "read the idea from a file")                                            // -f = file (docker, kubectl, tar, make)
	f.BoolVarP(&c.interactive, "interactive", "i", false, "open the form even when text was given")                // -i = interactive (docker, ssh, git add)
	f.BoolVarP(&c.ask, "ask", "a", false, "ask follow-up questions for missing info (default on a terminal)")      // lowercase enables
	f.BoolVarP(&c.noAsk, "no-ask", "A", false, "never ask; just report gaps")                                      // uppercase negates
	f.BoolVarP(&c.explain, "explain", "e", false, "add a plain-language summary of why (default: config explain)") // e = explain
	f.BoolVarP(&c.noExplain, "no-explain", "E", false, "skip the summary: one model call fewer")                   // uppercase negates, as -a/-A
	f.DurationVarP(&c.timeout, "timeout", "t", 0, "per-question timeout, e.g. 20s")                                // -t = timeout (ping, nc); never "type"
	f.BoolVar(&c.sequential, "sequential", false, "debug: one question at a time")                                 // debug-only: no short form on purpose
}

// overrides turns flags the user actually passed into config keys (flags > env > files).
func (a *app) overrides(c *checkFlags) map[string]any {
	o := map[string]any{}
	if c.backend != "" {
		o["backend"] = c.backend
	}
	if c.explain || c.noExplain {
		o["explain"] = c.explain && !c.noExplain
	}
	if c.timeout > 0 {
		o["timeouts.question"] = c.timeout.String()
	}
	if lvl := logging.Level(a.global.verbose, a.global.quiet); lvl != "" {
		o["log.level"] = lvl
	}
	if a.global.logFormat != "" {
		o["log.format"] = a.global.logFormat
	}
	return o
}

// loadConfig loads twice when -m is given: the model override is keyed by the
// backend, which is only known once flags, env and files have been merged.
func (a *app) loadConfig(c *checkFlags) (config.Config, error) {
	o := a.overrides(c)
	cfg, err := config.Load(a.files(), config.LoadOptions{Environ: a.environ, Overrides: o})
	if err != nil || c.model == "" {
		return cfg, err
	}
	o["backends."+cfg.Backend+".model"] = c.model
	return config.Load(a.files(), config.LoadOptions{Environ: a.environ, Overrides: o})
}

func (a *app) runCheck(cmd *cobra.Command, args []string, c *checkFlags) error {
	format, err := outputFormat(c.output, c.json, a.stdoutTTY)
	if err != nil {
		return err
	}
	if a.global.noColor {
		tui.DisableColor()
	}
	intake, interactive, err := a.intake(args, c)
	if err != nil {
		return err
	}
	if format == formatPretty {
		return a.runApp(cmd.Context(), c, intake, interactive)
	}
	if interactive {
		return errors.New("no idea given: the interactive app needs a terminal; pass text, a file, or stdin")
	}
	engine, cfg, err := a.engine(c)
	if err != nil {
		return err
	}
	if err := ready(cmd.Context(), cfg); err != nil {
		return fmt.Errorf("%w\n(run `ideacheck setup` in a terminal to choose a provider, or pass -b / -m)", err)
	}
	log := logging.New(a.stderr, cfg.Log.Level, cfg.Log.Format, !a.global.noColor && a.getenv("NO_COLOR") == "")
	log.Info().Str("backend", cfg.Backend).Str("model", cfg.Active().Model).Msg("checking idea")
	res, err := engine.Check(cmd.Context(), intake, pipeline.Options{Rubric: c.rubric, Sequential: c.sequential})
	if err != nil {
		return err
	}
	log.Debug().Str("request_id", res.ID).Str("status", res.Status).Str("verdict", res.Verdict).Int64("total_ms", res.Timing.TotalMS).Msg("check finished")
	if err := a.persist(cmd.Context(), cfg.Store.Path, intake, res); err != nil {
		log.Warn().Err(err).Msg("result was not saved to history")
	}
	return a.show(res, format, false)
}

// engine builds the pipeline from the current effective config.
func (a *app) engine(c *checkFlags) (*pipeline.Engine, config.Config, error) {
	cfg, err := a.loadConfig(c)
	if err != nil {
		return nil, cfg, err
	}
	judge, err := backends.New(cfg, backends.Deps{Files: a.files(), Secret: a.secrets().Get})
	if err != nil {
		return nil, cfg, err
	}
	return &pipeline.Engine{Config: cfg, Files: a.files(), Judge: judge}, cfg, nil
}

func (a *app) secrets() config.Secrets { return config.Secrets{Getenv: a.getenv, Dir: a.files().Dir} }

// runApp is human mode: the paged app. With an idea in hand it opens straight on
// the live check; with none (or -i) it opens on the menu / the idea steps.
func (a *app) runApp(ctx context.Context, c *checkFlags, intake pipeline.Intake, interactive bool) error {
	start := tui.Start{Page: tui.OpenCheck, Intake: intake, NoAsk: c.noAsk, NeedSetup: a.needsSetup(c),
		Options: pipeline.Options{Rubric: c.rubric, Sequential: c.sequential}}
	switch {
	case interactive && intake.Idea == "":
		start.Page = tui.OpenMenu
	case interactive:
		start.Page = tui.OpenIdea
	}
	return a.openApp(ctx, c, start)
}

// openApp runs the app and leaves the last result in the terminal's scrollback.
func (a *app) openApp(ctx context.Context, c *checkFlags, start tui.Start) error {
	res, err := tui.Run(ctx, &host{app: a, flags: c, ctx: ctx}, start, a.stdout)
	if err != nil || res == nil {
		return err
	}
	_, err = fmt.Fprint(a.stdout, tui.RenderResult(res))
	return err
}

// intake resolves the idea from args/file/stdin and merges the saved profile.
// interactive is true when the idea should be collected (or edited) in the app.
func (a *app) intake(args []string, c *checkFlags) (in pipeline.Intake, interactive bool, err error) {
	src, err := resolveInput(args, c.file, a.input)
	if err != nil {
		return in, false, err
	}
	if !src.Interactive {
		if in, err = pipeline.ParseIntake(src.Data); err != nil {
			return in, false, err
		}
	}
	in, err = a.withProfile(in, c.profile)
	return in, src.Interactive || c.interactive, err
}

// needsSetup: nothing tells us which provider to use — no user config, no -b,
// no IDEACHECK_BACKEND.
func (a *app) needsSetup(c *checkFlags) bool {
	return !config.HasUserConfig(a.files().Dir) && c.backend == "" && a.getenv("IDEACHECK_BACKEND") == ""
}

func (a *app) saveProfile(profile map[string]string) error {
	path := a.profilePath("")
	b, err := yaml.Marshal(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// withProfile fills profile fields the intake did not set from the saved profile
// (profile.yaml), a named one (profiles/NAME.yaml), or an explicit file.
func (a *app) withProfile(in pipeline.Intake, flag string) (pipeline.Intake, error) {
	path := a.profilePath(flag)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && flag == "" {
		return in, nil
	}
	if err != nil {
		return in, fmt.Errorf("profile: %w", err)
	}
	saved := map[string]string{}
	if err := yaml.Unmarshal(b, &saved); err != nil {
		return in, fmt.Errorf("profile %s: %w", path, err)
	}
	for field, value := range saved {
		if !pipeline.KnownField("profile." + field) {
			return in, fmt.Errorf("profile %s: %q is not a profile field (allowed: %v)", path, field, pipeline.ProfileFields())
		}
		if in.Profile[field] == "" {
			in = in.With("profile."+field, value)
		}
	}
	return in, nil
}

func (a *app) profilePath(flag string) string {
	dir := config.UserDir(a.getenv, a.home)
	switch {
	case flag == "":
		return filepath.Join(dir, "profile.yaml")
	case a.input.IsFile(flag):
		return flag
	}
	return filepath.Join(dir, "profiles", flag+".yaml")
}
