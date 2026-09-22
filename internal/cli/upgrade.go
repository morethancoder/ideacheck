package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/selfupdate"
	"github.com/morethancoder/ideacheck/internal/ui"
)

// upgradeTimeout bounds the whole upgrade: two downloads on a slow line.
const upgradeTimeout = 5 * time.Minute

// noUpdateCheck turns the daily background lookup off for good.
const noUpdateCheck = "IDEACHECK_NO_UPDATE_CHECK"

func (a *app) upgradeCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "update ideacheck to the latest release",
		Long: `Update ideacheck to the latest release on GitHub.

The download is checked against the release's published checksums before it
replaces the binary you are running. An ideacheck installed by Homebrew or
` + "`go install`" + ` is left alone, with the right command to upgrade it printed instead.

ideacheck also looks for a new release once a day in the background and mentions
one in a single line after a check. Set ` + noUpdateCheck + `=1 to turn that off.`,
		Aliases: []string{"update"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runUpgrade(cmd.Context(), checkOnly)
		},
	}
	// Long form only: -c is the global config directory, and -C/-u have no
	// convention that fits "just tell me, do not install".
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report whether a newer release exists, without installing it")
	return cmd
}

func (a *app) runUpgrade(ctx context.Context, checkOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, upgradeTimeout)
	defer cancel()

	p := a.printer(a.stdout)
	defer p.Stop() // an error path leaves no half-drawn line behind

	current := selfupdate.Clean(Version)
	p.Title("ideacheck", "upgrade")
	p.Row("current", current)

	p.Pending("latest", "asking GitHub for the newest release")
	rel, err := selfupdate.Latest(ctx, http.DefaultClient, a.releasesURL)
	if err != nil {
		return err
	}
	p.Row("latest", rel.Version)

	switch {
	case rel.Version == current:
		p.Done("ideacheck %s is the latest release", current)
		return nil
	case checkOnly:
		p.Done("ideacheck %s is available — you have %s", rel.Version, current)
		whatChanged(p, rel)
		p.Hint("ideacheck upgrade", "install it")
		p.Hint("release notes", rel.Page)
		p.Blank()
		return nil
	}

	exe, err := a.exe()
	if err != nil {
		return err
	}
	// Follow symlinks: the install script and most package managers put a link
	// on PATH, and the binary to replace is the one it points at.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if manager, command := selfupdate.Manager(exe, a.getenv); manager != "" {
		return fmt.Errorf("this ideacheck was installed with %s, which should be the one to replace it:\n  %s", manager, command)
	}

	if err := selfupdate.Install(ctx, http.DefaultClient, rel, exe, func(ev selfupdate.Progress) {
		a.upgradeStep(p, ev)
	}); err != nil {
		return err
	}
	p.Done("ideacheck %s is ready", rel.Version)
	whatChanged(p, rel)
	p.Hint("ideacheck", "check an idea, or open the app")
	p.Hint("release notes", rel.Page)
	p.Blank()
	return nil
}

// maxNotes is how many changes an upgrade lists before pointing at the release
// page for the rest. A release is a screenful of commits at most; the page is
// for reading, the column is for knowing whether to.
const maxNotes = 10

// whatChanged lists the headlines of a release under the done line, so the
// user learns what they just got (or would get) without leaving the terminal.
// A release with no notes prints nothing here; the link below still applies.
func whatChanged(p *ui.Printer, rel selfupdate.Release) {
	if len(rel.Notes) == 0 {
		return
	}
	p.Head("What changed")
	shown := rel.Notes
	if len(shown) > maxNotes {
		shown = shown[:maxNotes]
	}
	for _, note := range shown {
		p.Item(note)
	}
	if more := len(rel.Notes) - len(shown); more > 0 {
		p.Note("and %d more — see the release notes", more)
	}
	p.Blank()
}

// upgradeStep draws one install step in the installer's own vocabulary: a
// pending line while it runs, a bar while bytes move, a row when it is done.
func (a *app) upgradeStep(p *ui.Printer, ev selfupdate.Progress) {
	switch {
	case ev.Finished:
		p.Row(ev.Step, upgradeDone(ev, a.home))
	case ev.Note != "":
		p.Pending(ev.Step, ev.Note)
	default:
		p.Progress(ev.Step, ev.Done, ev.Total)
	}
}

// upgradeDone is what a finished step leaves in its row: the size it fetched,
// the path it wrote, or the sentence the step itself supplied.
func upgradeDone(ev selfupdate.Progress, home string) string {
	switch ev.Step {
	case "download":
		return ui.Bytes(ev.Done)
	case "install":
		return shortPath(ev.Note, home)
	}
	return ev.Note
}

// shortPath writes a path under the home directory the way the shell shows it,
// because "~/.local/bin/ideacheck" is the form the user recognizes.
func shortPath(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// startUpdateCheck begins the once-a-day release lookup and returns the
// function that reports the answer, or nil when this is a run where a notice
// would be unwelcome: an unreleased build, output that is not a terminal, CI, a
// command that is already about versions, or the opt-out set.
//
// It runs before the flags are parsed, so it deliberately reads nothing from
// the config: the answer is remembered in the user's state directory whatever
// --config says, because it is state about this machine, not configuration.
func (a *app) startUpdateCheck(ctx context.Context, args []string) func() *selfupdate.Found {
	switch {
	case a.getenv(noUpdateCheck) != "", a.getenv("CI") != "":
		return nil
	case !a.stdoutTTY, !selfupdate.Released(Version), aboutVersions(args):
		return nil
	}
	return selfupdate.Checker{
		Dir:     config.UserDir(a.getenv, a.home),
		Current: Version,
		URL:     a.releasesURL,
	}.Start(ctx)
}

// reportUpdate prints the one line, after the command has had its say.
func (a *app) reportUpdate(check func() *selfupdate.Found) {
	if check == nil || a.global.quiet {
		return
	}
	if found := check(); found != nil {
		p := a.printer(a.stderr)
		p.Blank()
		p.Note("ideacheck %s is available (you have %s) — run `ideacheck upgrade`.", found.Version, selfupdate.Clean(Version))
		p.Stop()
	}
}

// aboutVersions reports whether the run is already about versions or upgrades,
// where a notice would be either redundant or in the way of parseable output.
func aboutVersions(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--":
			return false // everything after this is idea text, not a command
		case "upgrade", "update", "help", "--help", "-h", "--version", "-V", "--quiet":
			return true
		}
		// -q and -vq alike: a quiet run prints nothing extra.
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "q") {
			return true
		}
	}
	return false
}
