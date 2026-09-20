package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/rubric"
	"github.com/morethancoder/ideacheck/internal/tui"
)

func (a *app) rubricsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "rubrics", Short: "list available rubrics and their questions", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load(a.files(), config.LoadOptions{Environ: a.environ})
			if err != nil {
				return err
			}
			names, err := rubric.Names(a.files(), cfg.RubricsDir)
			if err != nil {
				return err
			}
			for _, name := range names {
				rb, err := rubric.Load(a.files(), cfg.RubricsDir, name)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.stdout, "%s — %s\n", rb.Name, rb.Description)
				for _, q := range rb.Questions {
					fmt.Fprintf(a.stdout, "  %-26s %-6s weight %.1f  %s\n", q.ID, q.Kind, q.Weight, q.Instructions)
				}
			}
			return nil
		},
	}
}

// pageCmd opens the app on one page and exits when that task is done.
func (a *app) pageCmd(use, short string, aliases []string, open func() tui.Start) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Aliases: aliases, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.stdoutTTY {
				return fmt.Errorf("`%s` is interactive and needs a terminal", use)
			}
			return a.openApp(cmd.Context(), &checkFlags{}, open())
		},
	}
}

func (a *app) profileCmd() *cobra.Command {
	return a.pageCmd("profile", "view/edit the saved personal profile", nil,
		func() tui.Start { return tui.Start{Page: tui.OpenProfile, ExitAfter: true} })
}

func (a *app) setupCmd() *cobra.Command {
	return a.pageCmd("setup", "choose the model provider, API key and model", []string{"settings"},
		func() tui.Start { return tui.Start{Page: tui.OpenSetup, ExitAfter: true} })
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "config path, config dump [dir], config edit"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "path", Short: "print the config directory in use", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				_, err := fmt.Fprintln(a.stdout, a.files().Dir)
				return err
			},
		},
		&cobra.Command{
			Use: "dump [dir]", Short: "write the effective config files to a directory for editing", Args: cobra.MaximumNArgs(1),
			RunE: func(_ *cobra.Command, args []string) error {
				dir := a.files().Dir
				if len(args) == 1 {
					dir = args[0]
				}
				return a.dump(dir)
			},
		},
		&cobra.Command{
			Use: "edit", Short: "open config.yaml in $EDITOR (not built yet)", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("`config edit` is not built yet; run `ideacheck config dump` and edit %s", filepath.Join(a.files().Dir, "config.yaml"))
			},
		},
	)
	return cmd
}

// dump writes every effective file that does not already exist in dir; a file
// the user has edited is never overwritten.
func (a *app) dump(dir string) error {
	files := a.files()
	names, err := files.All()
	if err != nil {
		return err
	}
	for _, name := range names {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(a.stdout, "kept     %s\n", target)
			continue
		}
		b, err := files.Read(name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "wrote    %s\n", target)
	}
	return nil
}
