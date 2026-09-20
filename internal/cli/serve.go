package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/ideacheck/internal/judge/backends"
	"github.com/morethancoder/ideacheck/internal/logging"
	"github.com/morethancoder/ideacheck/internal/pipeline"
	"github.com/morethancoder/ideacheck/internal/server"
	"github.com/morethancoder/ideacheck/internal/store"
)

func (a *app) serveCmd() *cobra.Command {
	var port int
	var host, backend string
	var allowCLI bool
	cmd := &cobra.Command{
		Use: "serve", Aliases: []string{"server"}, Short: "run the local HTTP API", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig(&checkFlags{backend: backend})
			if err != nil {
				return err
			}
			if backends.HumanOnly(cfg.Backend) && !allowCLI {
				return fmt.Errorf("the %s backend is for human CLI use (it spends your personal login's limits); pass --allow-cli-backend to serve it anyway", cfg.Backend)
			}
			judge, err := backends.New(cfg, backends.Deps{Files: a.files(), Secret: a.secrets().Get})
			if err != nil {
				return err
			}
			st, err := store.Open(store.ExpandHome(cfg.Store.Path, a.home))
			if err != nil {
				return err
			}
			defer st.Close()
			log := logging.New(a.stderr, cfg.Log.Level, cfg.Log.Format, !a.global.noColor && a.getenv("NO_COLOR") == "")
			api := &server.Server{Engine: &pipeline.Engine{Config: cfg, Files: a.files(), Judge: judge}, Store: st, Files: a.files(), RubricsDir: cfg.RubricsDir, Log: log}
			return listen(cmd.Context(), net.JoinHostPort(host, strconv.Itoa(port)), api.Handler(), func(addr string) {
				log.Info().Str("addr", "http://"+addr).Str("backend", cfg.Backend).Str("model", cfg.Active().Model).Msg("serving")
			})
		},
	}
	f := cmd.Flags()
	f.IntVarP(&port, "port", "p", 8080, "port")                                                                   // here -p IS port (docker, ssh, kubectl port-forward)
	f.StringVarP(&host, "host", "H", "127.0.0.1", "bind address")                                                 // -H = host; -h is help
	f.StringVarP(&backend, "backend", "b", "", "jev, logprob, structured, claude-cli, mock")                      // same as the default action
	f.BoolVar(&allowCLI, "allow-cli-backend", false, "permit the claude-cli / codex-cli backends in server mode") // long only: a deliberate opt-in
	return cmd
}

// listen serves until interrupted, then drains in-flight checks.
func listen(ctx context.Context, addr string, h http.Handler, ready func(string)) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ready(ln.Addr().String())
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return srv.Shutdown(drain)
}
