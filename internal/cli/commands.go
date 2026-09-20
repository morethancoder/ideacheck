package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/pipeline"
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

// fieldsCmd is how a caller discovers what to send. The meanings come from
// configs/fields.yaml, which this command, its JSON form and the extraction
// prompt all read, so they cannot drift apart.
func (a *app) fieldsCmd() *cobra.Command {
	var output string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "fields", Short: "what you can tell ideacheck about an idea, and what each field means", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fields, err := pipeline.LoadFields(a.files())
			if err != nil {
				return err
			}
			format, err := outputFormat(output, asJSON, a.stdoutTTY)
			if err != nil {
				return err
			}
			if format == formatJSON {
				enc := json.NewEncoder(a.stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(fields)
			}
			return a.printFields(fields)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "pretty (default on a terminal), json, plain")
	cmd.Flags().BoolVar(&asJSON, "json", false, "alias for -o json")
	return cmd
}

func (a *app) printFields(f *pipeline.Fields) error {
	var b strings.Builder
	b.WriteString("About the idea — give what you know. Anything you leave out is read from the document when there is one, and otherwise comes back in missing[].\n\n")
	writeFields(&b, f.Idea)
	b.WriteString("\nAbout you — without these, founder-fit questions are left unscored rather than guessed.\n\n")
	writeFields(&b, f.Profile)
	b.WriteString("\nOne call, no file:\n")
	b.WriteString("  ideacheck --agent --answer problem=\"...\" --answer audience=\"...\" --answer why_now=\"...\"\n")
	_, err := io.WriteString(a.stdout, b.String())
	return err
}

func writeFields(b *strings.Builder, fields []pipeline.Field) {
	for _, f := range fields {
		fmt.Fprintf(b, "  %s\"...\"\n      %s\n", f.Flag, f.Description)
		if f.Example != "" {
			fmt.Fprintf(b, "      e.g. %s\n", f.Example)
		}
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

// profileCmd opens the form on a terminal. Its subcommands do the same job
// without one: a profile that can only be set in a TUI cannot be set by the
// caller who needs it most, and founder-fit questions then go unscored forever.
func (a *app) profileCmd() *cobra.Command {
	cmd := a.pageCmd("profile", "view/edit the saved personal profile", nil,
		func() tui.Start { return tui.Start{Page: tui.OpenProfile, ExitAfter: true} })
	cmd.AddCommand(
		&cobra.Command{
			Use: "show", Short: "print the saved profile", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error { return a.showProfile() },
		},
		&cobra.Command{
			Use: "set FIELD=VALUE [FIELD=VALUE ...]", Short: `save profile fields, e.g. profile set background="solo dev, ten years in payroll"`,
			Args: cobra.MinimumNArgs(1),
			RunE: func(_ *cobra.Command, args []string) error { return a.setProfile(args) },
		},
	)
	return cmd
}

func (a *app) showProfile() error {
	saved, err := a.readProfile("")
	if err != nil {
		return err
	}
	if len(saved) == 0 {
		_, err := fmt.Fprintf(a.stdout, "no profile saved (%s)\nset one with `ideacheck profile set background=\"...\"`, or `ideacheck profile` on a terminal\n", a.profilePath(""))
		return err
	}
	for _, field := range pipeline.ProfileFields() {
		if v := saved[field]; v != "" {
			fmt.Fprintf(a.stdout, "%-18s %s\n", field, v)
		}
	}
	return nil
}

// setProfile merges the given fields into the saved profile: what is not named
// is left as it was, and an empty value clears a field.
func (a *app) setProfile(args []string) error {
	saved, err := a.readProfile("")
	if err != nil {
		return err
	}
	if saved == nil {
		saved = map[string]string{}
	}
	for _, kv := range args {
		field, value, ok := strings.Cut(kv, "=")
		field = strings.TrimSpace(field)
		if !ok || field == "" {
			return fmt.Errorf("%q: want field=value", kv)
		}
		if !pipeline.KnownField(profileField(field)) {
			return fmt.Errorf("%q is not a profile field (allowed: %s)", field, strings.Join(pipeline.ProfileFields(), ", "))
		}
		if value = strings.TrimSpace(value); value == "" {
			delete(saved, field)
			continue
		}
		saved[field] = value
	}
	if err := a.saveProfile(saved); err != nil {
		return err
	}
	return a.showProfile()
}

func (a *app) setupCmd() *cobra.Command {
	return a.pageCmd("setup", "choose the model provider, API key and model", []string{"settings"},
		func() tui.Start { return tui.Start{Page: tui.OpenSetup, ExitAfter: true} })
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "config path, config dump [dir], config set KEY VALUE, config edit"}
	cmd.AddCommand(
		&cobra.Command{
			Use: "path", Short: "print the config directory in use", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				_, err := fmt.Fprintln(a.stdout, a.files().Dir)
				return err
			},
		},
		a.dumpCmd(),
		a.setCmd(),
		&cobra.Command{
			Use: "edit", Short: "open config.yaml in $EDITOR (not built yet)", Args: cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("`config edit` is not built yet; run `ideacheck config dump` and edit %s", filepath.Join(a.files().Dir, "config.yaml"))
			},
		},
	)
	return cmd
}

// dumpCmd prints the effective config by default. Writing is the unusual case
// and needs a directory said out loud, because a copy in the live config dir
// shadows the built-in default from then on — including after an upgrade.
func (a *app) dumpCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use: "dump [dir]", Short: "print the effective config files, or write them to a directory for editing", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.printConfig()
			}
			live, err := filepath.Abs(a.files().Dir)
			if err != nil {
				return err
			}
			target, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			if target == live && !force {
				return fmt.Errorf("%s is the config directory ideacheck reads: a copy there overrides the built-in default from now on, upgrades included.\nWrite somewhere else, or pass --force if shadowing them is what you want", live)
			}
			return a.dump(args[0])
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow writing into the config directory ideacheck reads")
	return cmd
}

// setCmd keeps a choice that -b / -m would otherwise make for one run only. A
// value that does not load is undone rather than left in place.
func (a *app) setCmd() *cobra.Command {
	return &cobra.Command{
		Use: "set KEY VALUE", Short: "set one key in your config.yaml, e.g. `config set backends.claude-cli.model opus`", Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := a.files().Dir
			before, had := config.ReadMain(dir)
			if err := config.SetKey(dir, args[0], args[1]); err != nil {
				return err
			}
			if _, err := config.Load(a.files(), config.LoadOptions{Environ: a.environ}); err != nil {
				return errors.Join(fmt.Errorf("%s: %w", args[0], err), config.Restore(dir, before, had))
			}
			_, err := fmt.Fprintf(a.stdout, "%s = %s  (%s)\n", args[0], args[1], filepath.Join(dir, "config.yaml"))
			return err
		},
	}
}

// printConfig writes every effective file to stdout, each under its path, so
// the output can be read, diffed or piped without touching the config dir.
func (a *app) printConfig() error {
	files := a.files()
	names, err := files.All()
	if err != nil {
		return err
	}
	for _, name := range names {
		b, err := files.Read(name)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "# ==> %s <==\n%s\n", name, b); err != nil {
			return err
		}
	}
	return nil
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
