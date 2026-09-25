package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/backends"
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/logging"
	"github.com/morethancoder/ideacheck/internal/tui"
	"github.com/morethancoder/ideacheck/search"
	"github.com/morethancoder/ideacheck/store"
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
	writer      string
	research    bool
	noResearch  bool
	timeout     time.Duration
	sequential  bool
	answers     []string
	context     string
	profileText string
	partial     bool
	strict      bool
	agent       bool
}

func (c *checkFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVarP(&c.output, "output", "o", "", "pretty (default on a terminal), json, plain")                                                          // -o = output format (kubectl, gcc, curl)
	f.BoolVar(&c.json, "json", false, "alias for -o json")                                                                                             // no -j: that means jobs in make/cargo/ninja
	f.StringVarP(&c.backend, "backend", "b", "", "jev, laya, logprob, structured, claude-cli, codex-cli, mock")                                        // b = backend
	f.StringVarP(&c.model, "model", "m", "", "override the backend's model")                                                                           // -m = model in llm, ollama
	f.StringVarP(&c.rubric, "rubric", "r", "", "force a rubric instead of auto-routing")                                                               // r = rubric
	f.StringVarP(&c.profile, "profile", "P", "", "use a named or alternate profile file")                                                              // -P: lowercase -p is port/print nearly everywhere
	f.StringVarP(&c.file, "file", "f", "", "read the idea from a file")                                                                                // -f = file (docker, kubectl, tar, make)
	f.BoolVarP(&c.interactive, "interactive", "i", false, "open the form even when text was given")                                                    // -i = interactive (docker, ssh, git add)
	f.BoolVarP(&c.ask, "ask", "a", false, "ask follow-up questions for missing info (default on a terminal)")                                          // lowercase enables
	f.BoolVarP(&c.noAsk, "no-ask", "A", false, "never ask: report the gaps and score around them")                                                     // uppercase negates
	f.BoolVarP(&c.explain, "explain", "e", false, "add a plain-language summary of why (default: config explain)")                                     // e = explain
	f.BoolVarP(&c.noExplain, "no-explain", "E", false, "skip the summary: one model call fewer")                                                       // uppercase negates, as -a/-A
	f.StringVarP(&c.writer, "writer", "w", "", "backend that reads, researches and writes, next to a -b that only judges (e.g. -b jev -w claude-cli)") // w = writer; curl's -w (write-out) is about output too, and nothing here prints a format
	// Long-only: -r is the rubric, and -s/-S mean silent in curl and ssh.
	f.BoolVar(&c.research, "research", false, "look the idea up on the web before scoring (default: config research.enabled, when the writer can search)")
	f.BoolVar(&c.noResearch, "no-research", false, "score the description alone: no web lookup")
	f.DurationVarP(&c.timeout, "timeout", "t", 0, "per-question timeout, e.g. 20s") // -t = timeout (ping, nc); never "type"
	f.BoolVar(&c.sequential, "sequential", false, "debug: one question at a time")  // debug-only: no short form on purpose
	// Facts and modes for a caller with no terminal to answer in. All long-only:
	// the sensible letters are taken, and an agent writes the long form anyway.
	f.StringArrayVar(&c.answers, "answer", nil, `fill a field instead of being asked: --answer why_now="the models got cheap" (repeatable)`)
	f.StringVar(&c.context, "context", "", "supporting material: what exists today, where the idea came from")
	f.StringVar(&c.profileText, "profile-text", "", "your background in one line, instead of the saved profile")
	f.BoolVar(&c.partial, "partial", false, "score the idea even when facts are missing (the default when nothing can ask)")
	f.BoolVar(&c.strict, "strict", false, "stop with needs_input (exit 2) when a fact is missing")
	f.BoolVar(&c.agent, "agent", false, "agent mode: JSON on stdout, never ask, score around gaps, status in the exit code")
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
	if c.writer != "" {
		o["writer"] = c.writer
	}
	if c.research || c.noResearch {
		o["research.enabled"] = c.research && !c.noResearch
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
	format, err := outputFormat(c.output, c.json || c.agent, a.stdoutTTY)
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
	// Nothing here can ask a follow-up, so a missing fact discounts the
	// confidence and is reported in missing[]; it never withholds the verdict
	// unless the caller asked for that with --strict.
	res, err := engine.Check(cmd.Context(), intake, ideacheck.CheckOptions{Rubric: c.rubric, Sequential: c.sequential, Proceed: !c.strict})
	if err != nil {
		return err
	}
	log.Debug().Str("request_id", res.ID).Str("status", res.Status).Str("verdict", res.Verdict).Int64("total_ms", res.Timing.TotalMS).Msg("check finished")
	if err := a.persist(cmd.Context(), cfg.Store.Path, intake, res); err != nil {
		log.Warn().Err(err).Msg("result was not saved to history")
	}
	if err := a.show(res, format, false); err != nil {
		return err
	}
	return statusExit(res)
}

// statusExit turns the result's status into the process exit code, so an agent
// can branch without parsing anything: 0 scored, 2 nothing scored because facts
// are missing, 3 the check ran but produced no score.
func statusExit(res *ideacheck.Result) error {
	switch res.Status {
	case ideacheck.StatusNeedsInput:
		return exitError{code: 2, msg: fmt.Sprintf("needs input: %d fact(s) are not stated; supply them with --answer, or score without them with --partial", len(res.Missing))}
	case ideacheck.StatusError:
		return exitError{code: 3, msg: res.Error}
	}
	return nil
}

// engine builds the pipeline from the current effective config.
func (a *app) engine(c *checkFlags) (*ideacheck.Engine, config.Config, error) {
	cfg, err := a.loadConfig(c)
	if err != nil {
		return nil, cfg, err
	}
	engine, err := a.newEngine(cfg)
	return engine, cfg, err
}

// newEngine builds the judge, the writer when it is a different backend, and
// the findings cache: every command that checks ideas goes through here.
func (a *app) newEngine(cfg config.Config) (*ideacheck.Engine, error) {
	deps := backends.Deps{Files: a.files(), Secret: a.secrets().Get}
	judge, err := backends.New(cfg, deps)
	if err != nil {
		return nil, err
	}
	writer, err := backends.NewWriter(cfg, deps)
	if err != nil {
		return nil, err
	}
	o := ideacheck.Options{Settings: cfg.Settings(), Files: a.files(), Judge: judge, Writer: writer,
		Cache: findingsCache{path: store.ExpandHome(cfg.Store.Path, a.home)}}
	if cfg.Research.Enabled {
		// A nil provider is not an error: the writer's own web tool searches, or
		// nothing does and the description is scored as it is.
		provider, err := search.New(context.Background(), cfg.Research.Searching(), deps.Secret)
		if err != nil {
			return nil, err
		}
		if provider != nil {
			o.Search, o.Pages = provider, search.NewReader(cfg.Research.PageTimeout)
		}
	}
	return ideacheck.New(o)
}

func (a *app) secrets() config.Secrets { return config.Secrets{Getenv: a.getenv, Dir: a.files().Dir} }

// runApp is human mode: the paged app. With an idea in hand it opens straight on
// the live check; with none (or -i) it opens on the menu / the idea steps.
func (a *app) runApp(ctx context.Context, c *checkFlags, intake ideacheck.Intake, interactive bool) error {
	start := tui.Start{Page: tui.OpenCheck, Intake: intake, NoAsk: c.noAsk, NeedSetup: a.needsSetup(c),
		Options: ideacheck.CheckOptions{Rubric: c.rubric, Sequential: c.sequential, Proceed: c.partial && !c.strict}}
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
func (a *app) intake(args []string, c *checkFlags) (in ideacheck.Intake, interactive bool, err error) {
	src, err := resolveInput(args, c.file, a.input)
	if err != nil {
		return in, false, err
	}
	if !src.Interactive {
		if in, err = ideacheck.ParseIntake(src.Data); err != nil {
			return in, false, err
		}
	}
	if in, err = given(in, c); err != nil {
		return in, false, err
	}
	if in, err = a.withProfile(in, c.profile); err != nil {
		return in, false, err
	}
	// Flags alone can carry the whole idea: --answer problem=… audience=… with
	// no file and no stdin is a complete check, not an empty one.
	return in, (src.Interactive && in.Empty()) || c.interactive, nil
}

// given applies the facts passed on the command line. They are set before the
// saved profile is merged, which only fills what is still empty, so an explicit
// --answer always wins.
func given(in ideacheck.Intake, c *checkFlags) (ideacheck.Intake, error) {
	if c.context != "" {
		in.Context = c.context
	}
	for _, kv := range c.answers {
		field, value, ok := strings.Cut(kv, "=")
		field, value = strings.TrimSpace(field), strings.TrimSpace(value)
		if !ok || field == "" || value == "" {
			return in, fmt.Errorf("--answer %q: want field=value", kv)
		}
		if !ideacheck.KnownField(field) {
			return in, fmt.Errorf("--answer %q: %q is not an intake field (idea: %s; yours: profile.%s)",
				kv, field, strings.Join(ideacheck.IdeaFields(), ", "), strings.Join(ideacheck.ProfileFields(), ", profile."))
		}
		in = in.With(field, value)
	}
	if c.profileText != "" {
		in = in.With("profile.background", c.profileText)
	}
	return in, nil
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
func (a *app) withProfile(in ideacheck.Intake, flag string) (ideacheck.Intake, error) {
	saved, err := a.readProfile(flag)
	if err != nil {
		return in, err
	}
	for field, value := range saved {
		if in.Profile[field] == "" {
			in = in.With("profile."+field, value)
		}
	}
	return in, nil
}

// readProfile loads a saved profile, checking every field it names. A missing
// default profile is not an error; a named one that is missing is.
func (a *app) readProfile(flag string) (map[string]string, error) {
	path := a.profilePath(flag)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && flag == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	saved := map[string]string{}
	if err := yaml.Unmarshal(b, &saved); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	for field := range saved {
		if !ideacheck.KnownField(profileField(field)) {
			return nil, fmt.Errorf("profile %s: %q is not a profile field (allowed: %v)", path, field, ideacheck.ProfileFields())
		}
	}
	return saved, nil
}

func profileField(name string) string { return "profile." + name }

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
