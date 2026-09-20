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

	rel, err := selfupdate.Latest(ctx, http.DefaultClient, a.releasesURL)
	if err != nil {
		return err
	}
	current := selfupdate.Clean(Version)
	if rel.Version == current {
		_, err := fmt.Fprintf(a.stdout, "ideacheck %s is the latest release.\n", current)
		return err
	}
	if checkOnly {
		_, err := fmt.Fprintf(a.stdout, "ideacheck %s is available (you have %s).\nRun `ideacheck upgrade` to install it: %s\n", rel.Version, current, rel.Page)
		return err
	}

	exe, err := os.Executable()
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

	fmt.Fprintf(a.stdout, "upgrading ideacheck %s → %s …\n", current, rel.Version)
	if err := selfupdate.Install(ctx, http.DefaultClient, rel, exe); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "done — ideacheck %s is installed at %s\nWhat changed: %s\n", rel.Version, exe, rel.Page)
	return err
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
		fmt.Fprintf(a.stderr, "\nideacheck %s is available (you have %s) — run `ideacheck upgrade`.\n", found.Version, selfupdate.Clean(Version))
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
