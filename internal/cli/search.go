package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/search"
	"github.com/morethancoder/ideacheck/internal/ui"
)

// searchUpTimeout bounds `search up`: the first run downloads the image.
const searchUpTimeout = 10 * time.Minute

// searxngSettings is the container's settings.yml among the config files, so a
// user overrides it the way they override a rubric.
const searxngSettings = "searxng/settings.yml"

func (a *app) searchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search",
		Short: "run your own search engine in Docker, so research is free and needs no key",
		Long: `Research looks an idea up on the web before scoring it, and needs something
to search with. ` + "`ideacheck search up`" + ` starts a SearXNG in Docker on this machine —
free, no API key — where research.endpoints.searxng says ideacheck will look for
one. It listens on 127.0.0.1 only, comes back after a reboot, and is removed by
` + "`ideacheck search down`" + `.

Docker is the one requirement; when it is missing or not running, the command
says how to install or start it. Without Docker, set TAVILY_API_KEY or
BRAVE_API_KEY instead, or use a writer with its own web tool.

The image and container name are research.searxng in config.yaml; its settings
are ` + searxngSettings + ` among the config files (` + "`ideacheck config dump`" + `).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.searchStatus(cmd.Context()) },
	}
	cmd.AddCommand(
		&cobra.Command{Use: "up", Short: "start the local SearXNG (installs nothing but its Docker image)", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return a.searchUp(cmd.Context()) }},
		&cobra.Command{Use: "down", Short: "stop and remove the local SearXNG", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return a.searchDown(cmd.Context()) }},
		&cobra.Command{Use: "status", Short: "whether Docker, the container and the search answer", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return a.searchStatus(cmd.Context()) }},
	)
	return cmd
}

// localSearch is the container as the config describes it.
func (a *app) localSearch() (*search.Local, config.Config, error) {
	cfg, err := config.Load(a.files(), config.LoadOptions{Environ: a.environ})
	if err != nil {
		return nil, cfg, err
	}
	settings, err := a.files().Read(searxngSettings)
	if err != nil {
		return nil, cfg, err
	}
	return &search.Local{
		Image:     cfg.Research.SearXNG.Image,
		Container: cfg.Research.SearXNG.Container,
		Endpoint:  cfg.Research.Endpoints[search.SearXNG],
		Settings:  settings,
		Docker:    a.docker,
	}, cfg, nil
}

func (a *app) searchUp(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, searchUpTimeout)
	defer cancel()
	local, cfg, err := a.localSearch()
	if err != nil {
		return err
	}
	p := a.printer(a.stdout)
	defer p.Stop()
	p.Title("ideacheck", "search up")
	if err := local.Up(ctx, func(s search.Step) {
		switch {
		case s.Warn:
			p.Warn("%s", s.Text)
		case s.Done:
			p.Row(s.Label, s.Text)
		default:
			p.Pending(s.Label, s.Text)
		}
	}); err != nil {
		return err
	}
	p.Done("Research can search the web now")
	a.searchUsed(ctx, p, cfg)
	p.Hint("ideacheck \"idea\"", "check one: it is looked up first")
	p.Hint("ideacheck search", "how it is doing · `ideacheck search down` removes it")
	p.Blank()
	return nil
}

func (a *app) searchDown(ctx context.Context) error {
	local, _, err := a.localSearch()
	if err != nil {
		return err
	}
	p := a.printer(a.stdout)
	defer p.Stop()
	p.Pending("searxng", "removing "+local.Container)
	removed, err := local.Down(ctx)
	switch {
	case err != nil:
		return err
	case removed:
		p.Row("searxng", "stopped and removed "+local.Container)
	default:
		p.Skip("searxng", local.Container+" is not there")
	}
	return nil
}

func (a *app) searchStatus(ctx context.Context) error {
	local, cfg, err := a.localSearch()
	if err != nil {
		return err
	}
	p := a.printer(a.stdout)
	defer p.Stop()
	p.Title("ideacheck", "search")
	p.Pending("docker", "looking for Docker")
	state := local.Status(ctx)
	var noDocker *search.DockerError
	if errors.As(state.DockerErr, &noDocker) {
		p.Skip("docker", noDocker.Problem)
	} else if state.DockerErr != nil {
		p.Skip("docker", state.DockerErr.Error())
	} else {
		p.Row("docker", state.Docker)
		if state.Container == "" {
			p.Skip("container", local.Container+" is not there")
		} else {
			p.Row("container", local.Container+" "+state.Container)
		}
	}
	switch {
	case state.Answers:
		p.Row("searxng", "answers at "+local.Endpoint)
		if state.AnswerErr != nil {
			p.Warn("%v", state.AnswerErr)
		}
	case state.AnswerErr != nil:
		p.Skip("searxng", state.AnswerErr.Error())
	default:
		p.Skip("searxng", "nothing answers at "+local.Endpoint)
	}
	p.Blank()
	a.searchUsed(ctx, p, cfg)
	if !state.Answers {
		p.Hint("ideacheck search up", "start one in Docker: free, no key")
	}
	p.Blank()
	return nil
}

// searchUsed says what a check would search with right now — the question
// behind every one of these commands.
func (a *app) searchUsed(ctx context.Context, p *ui.Printer, cfg config.Config) {
	provider, err := search.New(ctx, cfg.Research, a.secrets().Get)
	switch {
	case !cfg.Research.Enabled:
		p.Note("research is off (research.enabled): checks score the description alone.")
	case err != nil:
		p.Warn("%v", err)
	case provider != nil:
		p.Note("research.search is %s: checks search with %s.", cfg.Research.Search, provider.Name())
	default:
		p.Note("research.search is %s: nothing to search with here, so the writer's own web tool is used, or the description alone.", cfg.Research.Search)
	}
}
