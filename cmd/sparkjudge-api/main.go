// Command sparkjudge-api is the hosted API behind the Sparkjudge iOS app: the
// ideacheck core with Jev judging and an OpenRouter model writing, behind App
// Attest, plans and quotas (internal/sparkjudge). docs/sparkjudge-api.md
// describes its routes, configuration and deployment.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/morethancoder/ideacheck/configs"
	"github.com/morethancoder/ideacheck/ideacheck"
	"github.com/morethancoder/ideacheck/internal/backends"
	"github.com/morethancoder/ideacheck/internal/config"
	"github.com/morethancoder/ideacheck/internal/sparkjudge"
	"github.com/morethancoder/ideacheck/judge/mock"
	"github.com/morethancoder/ideacheck/search"
	"github.com/morethancoder/ideacheck/server"
	"github.com/morethancoder/ideacheck/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sparkjudge-api:", err)
		os.Exit(1)
	}
}

func run() error {
	var dir, backend, attest, listen string
	flag.StringVar(&dir, "c", os.Getenv("SPARKJUDGE_CONFIG_DIR"), "directory whose files replace the embedded configs (sparkjudge.yaml, rubrics/, prompts/…)")
	flag.StringVar(&backend, "b", os.Getenv("SPARKJUDGE_BACKEND"), "one backend for both roles instead of engine.backend + engine.writer (mock: no model at all)")
	flag.StringVar(&attest, "attest", "", "required | off; overrides attest.mode")
	flag.StringVar(&listen, "listen", "", "address to serve on; overrides listen")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	files := configs.Over(dir)
	cfg, err := loadConfig(files, attest, listen)
	if err != nil {
		return err
	}
	engine, rubricsDir, err := newEngine(files, cfg, backend)
	if err != nil {
		return err
	}
	checks, err := store.Open(filepath.Join(cfg.DataDir, "checks.db"))
	if err != nil {
		return err
	}
	defer checks.Close()
	engine.Cache = findings{checks}
	accounts, err := sparkjudge.OpenAccounts(filepath.Join(cfg.DataDir, "accounts.db"))
	if err != nil {
		return err
	}
	defer accounts.Close()

	svc := &sparkjudge.Service{
		Config:      cfg,
		Accounts:    accounts,
		Verifier:    sparkjudge.Verifier{Roots: sparkjudge.AppleRoots(), AppID: cfg.Attest.AppID(), Development: cfg.Attest.Development},
		WebhookAuth: os.Getenv("REVENUECAT_WEBHOOK_AUTH"),
		Log:         log,
	}
	if svc.Lab, err = newLab(engine, files, cfg, checks); err != nil {
		return err
	}
	api := &server.Server{Engine: engine, Store: checks, Files: files, RubricsDir: rubricsDir, Log: log}
	if cfg.Attest.Mode == sparkjudge.AttestOff {
		log.Warn("attest is off: any caller who sends a user id is admitted; never run this in production")
	}
	if svc.WebhookAuth == "" {
		log.Warn("REVENUECAT_WEBHOOK_AUTH is not set: purchases will not reach this server")
	}
	return serve(cfg.Listen, svc.Handler(api), log)
}

func loadConfig(files configs.Files, attest, listen string) (sparkjudge.Config, error) {
	raw, err := files.Read(sparkjudge.ConfigFile)
	if err != nil {
		return sparkjudge.Config{}, err
	}
	cfg, err := sparkjudge.ParseConfig(raw, os.Getenv)
	if err != nil && attest == "" {
		return cfg, err
	}
	if attest != "" {
		cfg.Attest.Mode = attest
	}
	if listen != "" {
		cfg.Listen = listen
	}
	return cfg, cfg.Validate()
}

// newEngine builds the check engine the way the CLI does, from config.yaml
// with sparkjudge.yaml's engine: section over it. Keys come from the
// environment only.
func newEngine(files configs.Files, cfg sparkjudge.Config, backend string) (*ideacheck.Engine, string, error) {
	layer, err := cfg.EngineLayer()
	if err != nil {
		return nil, "", err
	}
	o := config.LoadOptions{Environ: os.Environ}
	if layer != nil {
		o.Layers = [][]byte{layer}
	}
	if backend != "" {
		o.Overrides = map[string]any{"backend": backend, "writer": ""}
	}
	ecfg, err := config.Load(files, o)
	if err != nil {
		return nil, "", err
	}
	for _, name := range []string{ecfg.Backend, ecfg.WriterName()} {
		if backends.HumanOnly(name) {
			return nil, "", fmt.Errorf("the %s backend spends a personal CLI login; the hosted API cannot use it", name)
		}
	}
	deps := backends.Deps{Files: files, Secret: os.Getenv}
	judge, err := backends.New(ecfg, deps)
	if err != nil {
		return nil, "", err
	}
	writer, err := backends.NewWriter(ecfg, deps)
	if err != nil {
		return nil, "", err
	}
	opts := ideacheck.Options{Settings: ecfg.Settings(), Files: files, Judge: judge, Writer: writer}
	if ecfg.Research.Enabled {
		provider, err := search.New(context.Background(), ecfg.Research.Searching(), deps.Secret)
		if err != nil {
			return nil, "", err
		}
		if provider != nil {
			opts.Search, opts.Pages = provider, search.NewReader(ecfg.Research.PageTimeout)
		}
	}
	engine, err := ideacheck.New(opts)
	return engine, ecfg.RubricsDir, err
}

// newLab gives the Lab routes the engine's roles: the judge types trend
// items, the writer mixes and writes sparks. The mock backend writes no text,
// so with it an OfflineWriter answers in the writer's place.
func newLab(engine *ideacheck.Engine, files configs.Files, cfg sparkjudge.Config, checks *store.Store) (*sparkjudge.Lab, error) {
	writer := engine.Writer
	if writer == nil {
		writer = engine.Judge
	}
	if m, ok := writer.(*mock.Judge); ok {
		writer = sparkjudge.OfflineWriter{Judge: m}
	}
	lab := &sparkjudge.Lab{Judge: engine.Judge, Writer: writer, Files: files, Settings: engine.Settings,
		Search: engine.Search, Cache: findings{checks}, Getenv: os.Getenv}
	return lab, lab.Load(cfg.Lab)
}

// findings keeps research findings in the checks database, shared by every
// user: they are what the public web says about an idea, and the same idea
// is not searched (and paid for) twice in a week.
type findings struct{ s *store.Store }

func (f findings) Get(ctx context.Context, key string, maxAge time.Duration) ([]byte, bool) {
	return f.s.Findings(ctx, key, maxAge)
}

func (f findings) Put(ctx context.Context, key string, b []byte) error {
	return f.s.KeepFindings(ctx, key, b)
}

// serve runs until SIGINT or SIGTERM, then lets checks in flight finish.
func serve(addr string, h http.Handler, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// No WriteTimeout: a check and its event stream take minutes.
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Info("serving", "addr", ln.Addr().String())
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	log.Info("draining checks in flight")
	drain, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
