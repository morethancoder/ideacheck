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

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
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
	releasesURL    string // GitHub release endpoint; empty means the real one

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
		stdoutTTY: isatty.IsTerminal(os.Stdout.Fd()),
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
		fmt.Fprintf(a.stderr, "ideacheck: %v\n", err)
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
	root.AddCommand(a.fieldsCmd(), a.rubricsCmd(), a.configCmd(), a.profileCmd(), a.setupCmd(), a.historyCmd(), a.lastCmd(), a.showCmd(), a.serveCmd(), a.benchCmd(), a.upgradeCmd())
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

  partial true means facts in missing[] were never stated: the scores stand and
  the confidence is discounted. Each missing entry names the field it fills, so
  the fix is --answer <fills>="...". extracted[] are fields read out of your
  document. A dimension with "error" was not scored (a founder-fit question with
  no profile, say) and its weight went to the others.

Picking the model
  -b BACKEND -m MODEL for one run; ` + "`ideacheck config set backends.<backend>.model <id>`" + ` to keep it.
  The backend and model are on the "checking idea" line on stderr and in .model.
`

// files resolves the override directory: --config, else the user config dir.
func (a *app) files() config.Files {
	dir := a.global.configDir
	if dir == "" {
		dir = config.UserDir(a.getenv, a.home)
	}
	return config.NewFiles(dir)
}
