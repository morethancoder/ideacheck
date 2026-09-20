// Package cli is the ideacheck command surface. HANDOFF.md §1a is authoritative
// for every command and flag here.
//
// Rules for adding a flag: the long form is a real word, the short form is one
// letter checked against the conventions in §1a, the rationale goes in a comment
// beside the flag, and the flag is added to the §1a table.
package cli

import (
	"context"
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
}

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
		fmt.Fprintf(a.stderr, "ideacheck: %v\n", err)
		code = 1
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
			if a.global.version {
				_, err := fmt.Fprintf(a.stdout, "ideacheck %s (%s)\n", Version, Commit)
				return err
			}
			return a.runCheck(cmd, args, check)
		},
	}
	a.addGlobalFlags(root)
	check.register(root)
	root.AddCommand(a.rubricsCmd(), a.configCmd(), a.profileCmd(), a.setupCmd(), a.historyCmd(), a.lastCmd(), a.showCmd(), a.serveCmd(), a.benchCmd(), a.upgradeCmd())
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
}

// files resolves the override directory: --config, else the user config dir.
func (a *app) files() config.Files {
	dir := a.global.configDir
	if dir == "" {
		dir = config.UserDir(a.getenv, a.home)
	}
	return config.NewFiles(dir)
}
