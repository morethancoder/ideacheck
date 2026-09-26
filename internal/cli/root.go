// Package cli is the ideacheck command surface. CLAUDE.md and the flag comments
// in check.go are authoritative for the flag surface.
//
// Rules for adding a flag: the long form is a real word, the short form is one
// letter checked against the conventions in CLAUDE.md, the rationale goes in a
// comment beside the flag, and the flag is documented in README.md.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/searchlocal"
	"github.com/morethancoder/ideacheck/internal/ui"
)

// Version and Commit are injected by `make build` via -ldflags.
var (
	Version = "dev"
	Commit  = "none"
)

const long = `ideacheck structures your thinking about an idea and flags what is missing.
It asks a judge model independent typed questions, combines the answers with
weights you control, and reports per-dimension scores plus a verdict
(build, explore, park, kill). It is NOT a success predictor.

Examples:
  ideacheck
  ideacheck "A CLI that scores startup ideas with a probability model"
  ideacheck idea.md -r side_project
  ideacheck "..." -b logprob -m qwen3:8b
  ideacheck "..." -o json | jq .verdict
  ideacheck PRODUCT.md --agent --answer why_now="agents read repos now"
  ideacheck --help-agent             (the whole contract for scripts and agents)
  cat ideas.txt | ideacheck -A -o json
  ideacheck setup                    (choose provider / API key / model)
  ideacheck search up                (a free search engine in Docker, for research)
  ideacheck serve -p 9000
  ideacheck bench -b structured,logprob -n 3

Use -- to force the next argument to be idea text: ideacheck -- "serve"`

// app carries process-wide dependencies so commands are testable.
type app struct {
	stdout, stderr io.Writer
	input          inputEnv
	environ        func() []string
	getenv         func(string) string
	home           string
	stdoutTTY      bool
	stderrTTY      bool
	executable     func() (string, error) // nil means os.Executable; the upgrade target
	width          int                    // terminal columns; 0 means ask the environment
	releasesURL    string                 // GitHub release endpoint; empty means the real one
	docker         searchlocal.Docker     // nil means the docker on PATH; `search up` drives it

	global globalFlags
}

type globalFlags struct {
	configDir string
	verbose   int
	quiet     bool
	version   bool
	noColor   bool
	logFormat string
	helpAgent bool
}

// exitError ends the process with a specific code without being a failure of
// the command itself: the check ran, and its status is the code.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	home, _ := os.UserHomeDir()
	a := &app{
		stdout: os.Stdout, stderr: os.Stderr,
		input: osInputEnv(), environ: os.Environ, getenv: os.Getenv, home: home,
		stdoutTTY:  isatty.IsTerminal(os.Stdout.Fd()),
		stderrTTY:  isatty.IsTerminal(os.Stderr.Fd()),
		width:      columns(),
		executable: os.Executable,
	}
	return a.run(context.Background(), os.Args[1:])
}

func (a *app) run(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	update := a.startUpdateCheck(ctx, args)
	code := 0
	if err := root.ExecuteContext(ctx); err != nil {
		var ex exitError
		if code = 1; errors.As(err, &ex) {
			code = ex.code
		}
		// One shape for every failure, and the same column the steps use: the
		// first line is the problem, the rest is what to do about it.
		p := a.printer(a.stderr)
		p.Fail("ideacheck: " + err.Error())
		p.Stop()
	}
	a.reportUpdate(update)
	return code
}

func (a *app) rootCmd() *cobra.Command {
	check := &checkFlags{}
	root := &cobra.Command{
		Use:           "ideacheck [idea text | file | -]",
		Short:         "Check an idea: typed questions, weighted scores, a verdict, and what is missing",
		Long:          long,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case a.global.version:
				_, err := fmt.Fprintf(a.stdout, "ideacheck %s (%s)\n", Version, Commit)
				return err
			case a.global.helpAgent:
				_, err := fmt.Fprint(a.stdout, agentHelp)
				return err
			}
			return a.runCheck(cmd, args, check)
		},
	}
	a.addGlobalFlags(root)
	check.register(root)
	root.AddCommand(a.fieldsCmd(), a.rubricsCmd(), a.configCmd(), a.profileCmd(), a.setupCmd(), a.historyCmd(), a.lastCmd(), a.showCmd(), a.serveCmd(), a.benchCmd(), a.searchCmd(), a.upgradeCmd())
	return root
}

func (a *app) addGlobalFlags(root *cobra.Command) {
	f, g := root.PersistentFlags(), &a.global
	f.StringVarP(&g.configDir, "config", "c", "", "config/override directory")             // -c = config, as in nginx
	f.CountVarP(&g.verbose, "verbose", "v", "more logging (-vv debug, -vvv trace)")        // universal
	f.BoolVarP(&g.quiet, "quiet", "q", false, "errors only")                               // universal
	f.BoolVarP(&g.version, "version", "V", false, "print version")                         // -V because -v is verbose (curl, ssh, python)
	f.BoolVar(&g.noColor, "no-color", false, "disable color (also honors NO_COLOR)")       // long only, by convention
	f.StringVar(&g.logFormat, "log-format", "", "log format: console or json (to stderr)") // long only
	// Only the default action has an agent mode, so this one is not persistent.
	root.Flags().BoolVar(&g.helpAgent, "help-agent", false, "how to call ideacheck from a script or an agent")
}

// agentHelp is the whole contract a caller with no terminal needs.
const agentHelp = `ideacheck for agents and scripts

Two ways to call it, best first:

  ideacheck --agent --answer problem="..." --answer audience="..." --answer why_now="..."
      Send the facts themselves. Most accurate: nothing has to be inferred, and
      it costs one model call less.

  ideacheck FILE --agent [--answer field=value ...]
      Send a document. One call reads the fields it already states (extracted[]
      says which); --answer fills anything the document leaves out.

Run ` + "`ideacheck fields`" + ` for every field and what it means, or
` + "`ideacheck fields -o json`" + ` for the same as data.

--agent means: JSON on stdout, logs on stderr, never ask a question, and score
the idea even when facts are missing. Every flag below works without it too.

Exit codes
  0  scored: read .verdict, .composite, .composite_confidence
  2  needs_input: nothing was scored because facts are missing (only with --strict)
  3  the check ran but no question could be scored; read .error
  1  the command itself failed (bad flag, no provider, unreadable file)

Giving it what it needs, instead of being asked
  --answer problem="..."            an idea field: title, problem, audience, solution,
                                    why_now, monetization, competitors_known, differentiation
  --answer profile.background="..." anything about you: skills, domains, network,
                                    would_use_myself, time_horizon, background
  --profile-text "solo dev, ex-designer"   shorthand for profile.background, this run only
  ideacheck profile set background="..."   save it once; every later run has it
  --context "what exists today ..."        supporting material that is not the pitch

Or put them in the document, as YAML frontmatter:
  ---
  context: what exists today ...
  fields: {why_now: "agents read repos now"}
  profile: {background: "solo dev, ex-designer"}
  ---
  the idea itself

Or pipe the whole intake as JSON:
  echo '{"idea":"...","fields":{"why_now":"..."},"profile":{"background":"..."}}' | ideacheck --agent

What comes back
  {"status":"ok","verdict":"explore","composite":0.62,"composite_confidence":0.71,
   "partial":true,"missing":[{"id":"has_why_now","ask":"What changed recently...","fills":"why_now"}],
   "extracted":["problem","audience"],"dimensions":[...],"top_strengths":[...],"top_risks":[...],
   "warnings":[...],"rubric":{"name":"business"},"model":"...","cost":{...}}

Research and the two roles
  Before scoring, the idea is looked up on the web (existing products, earlier
  attempts, market signals, recent changes) and the rubric reads what was found.
  ideacheck searches itself — a SearXNG at research.endpoints.searxng (free:
  ` + "`ideacheck search up`" + ` starts one in Docker), or Tavily / Brave when
  TAVILY_API_KEY / BRAVE_API_KEY is set — reads the best pages down to text, and
  the writer turns that into findings. With none of those, a writer
  with its own web tool searches (claude-cli, codex-cli, structured with provider
  anthropic or openrouter); with nothing, the description alone is scored.
  research.queries[] are the searches run; research.findings[] lists every
  finding with its url, how the judge typed it (relation) and whether it was
  used. The same idea reuses its findings for a week.
  --no-research            score the description alone
  -b jev -w claude-cli     one backend judges every typed question, another
                           reads, researches and writes (config: backend + writer)

  partial true means facts in missing[] were never stated: the scores stand and
  the confidence is discounted. Each missing entry names the field it fills, so
  the fix is --answer <fills>="...". extracted[] are fields read out of your
  document. A dimension with "error" was not scored (a founder-fit question with
  no profile, say) and its weight went to the others.

Picking the model
  -b BACKEND -m MODEL for one run; ` + "`ideacheck config set backends.<backend>.model <id>`" + ` to keep it.
  The backend and model are on the "checking idea" line on stderr and in .model.
`

// exe is the binary an upgrade replaces: this process, unless a test says
// otherwise. A test must never be able to overwrite the test binary.
func (a *app) exe() (string, error) {
	if a.executable != nil {
		return a.executable()
	}
	return os.Executable()
}

// printer returns the step column wired to this run: live redraws only where
// the writer really is a terminal, color unless the user or NO_COLOR said no.
func (a *app) printer(w io.Writer) *ui.Printer {
	tty := (w == a.stdout && a.stdoutTTY) || (w == a.stderr && a.stderrTTY)
	return ui.New(w, ui.Options{
		TTY:    tty,
		Color:  !a.global.noColor && a.getenv("NO_COLOR") == "",
		Width:  a.width,
		Getenv: a.getenv,
	})
}

// columns asks the terminal how wide it is, for the width of a progress bar.
// Zero means "unknown", which the printer reads as the usual eighty.
func columns() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return w
}

// files resolves the override directory: --config, else the user config dir.
func (a *app) files() config.Files {
	dir := a.global.configDir
	if dir == "" {
		dir = config.UserDir(a.getenv, a.home)
	}
	return config.NewFiles(dir)
}
