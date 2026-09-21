package search

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Local is the SearXNG ideacheck runs for the user: a Docker container on this
// machine, answering where research.endpoints.searxng says research will look.
// SearXNG is a Python service and cannot ride inside a Go binary; what the
// binary carries is the settings file and the knowledge of how to start it.
type Local struct {
	Image     string
	Container string
	Endpoint  string // research.endpoints.searxng
	Settings  []byte // its settings.yml: the defaults, with JSON output on
	Docker    Docker // nil means the docker on PATH
	Client    *http.Client
	GOOS      string        // whose install advice to give; empty means this machine's
	Wait      time.Duration // how long a new container gets to answer; 0 means a minute
}

// Docker runs one docker command and returns what it printed. env is added to
// the command's environment, which is how a secret reaches `docker create`
// without being an argument every `ps` can read.
type Docker func(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error)

// DockerCLI is the docker on PATH. Its error carries what docker said, because
// that is the only place "not running" and "not allowed" can be told apart.
func DockerCLI(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	var out, said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &said
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(said.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// DockerError is Docker not being usable, with what to do about it: the first
// line is the problem, the rest is the way out.
type DockerError struct {
	Problem string
	Advice  string
}

func (e *DockerError) Error() string { return e.Problem + "\n" + e.Advice }

const withoutDocker = "Without Docker, research can use a TAVILY_API_KEY or BRAVE_API_KEY instead."

// Ready reports the Docker server's version, or why there is none to talk to:
// not installed, installed but not running, or running but not for this user.
func (l *Local) Ready(ctx context.Context) (string, error) {
	version, err := l.docker(ctx, nil, nil, "version", "--format", "{{.Server.Version}}")
	if err == nil {
		return version, nil
	}
	goos := l.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	said := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "", &DockerError{"Docker is not installed", install[advice(goos)] + "\nThen start it and run this again.\n" + withoutDocker}
	case strings.Contains(said, "permission denied"):
		return "", &DockerError{"Docker is running, but this user may not use it",
			"Let your user talk to it: sudo usermod -aG docker $USER\nThen log out and in again, and run this again."}
	case strings.Contains(said, "cannot connect"), strings.Contains(said, "is the docker daemon running"),
		strings.Contains(said, "error during connect"), strings.Contains(said, "docker_engine"):
		return "", &DockerError{"Docker is installed but not running", start[advice(goos)] + "\nWait until it says it is running, then run this again."}
	}
	return "", fmt.Errorf("docker: %w", err)
}

// Install and start advice, per operating system. The URLs are Docker's own
// install pages (docs.docker.com, checked 2026-09-21).
var (
	install = map[string]string{
		"darwin":  "Install Docker Desktop: brew install --cask docker\n  or download it: https://docs.docker.com/desktop/setup/install/mac-install/",
		"windows": "Install Docker Desktop: winget install Docker.DockerDesktop\n  or download it: https://docs.docker.com/desktop/setup/install/windows-install/",
		"linux":   "Install Docker Engine: curl -fsSL https://get.docker.com | sh\n  or follow: https://docs.docker.com/engine/install/",
	}
	start = map[string]string{
		"darwin":  "Start it: open -a Docker",
		"windows": "Start it: open Docker Desktop from the Start menu",
		"linux":   "Start it: sudo systemctl start docker\n  (Docker Desktop: systemctl --user start docker-desktop)",
	}
)

func advice(goos string) string {
	if _, ok := install[goos]; ok {
		return goos
	}
	return "linux"
}

// Step is one thing Up did or is doing, for whoever draws the progress.
type Step struct {
	Label string
	Text  string
	Done  bool // false: in flight
	Warn  bool // worth knowing, and not a failure
}

// State is what Status found.
type State struct {
	Docker    string // server version; empty when Docker cannot be used
	DockerErr error  // why not
	Container string // docker's word for it (running, exited, created …); empty when there is none
	Answers   bool   // a SearXNG answers JSON searches at Endpoint
	AnswerErr error  // why not — or, with Answers, that its engines are rate-limiting it
}

// Up makes a SearXNG answer at Endpoint: ours if it is already running, one
// somebody else runs there if it takes JSON, else a new container.
func (l *Local) Up(ctx context.Context, step func(Step)) error {
	port, err := l.port()
	if err != nil {
		return err
	}
	step(Step{Label: "docker", Text: "looking for Docker"})
	version, err := l.Ready(ctx)
	if err != nil {
		return err
	}
	step(Step{Label: "docker", Text: version, Done: true})

	status, published := l.status(ctx)
	switch {
	case status == "running" && published == port:
		step(Step{Label: "searxng", Text: "already running; checking it answers"})
		return l.report(l.answer(ctx), "already running at "+l.Endpoint, step)
	case status == "" && l.searx().Reachable(ctx):
		// Not ours, but there: starting a second one on the same port cannot work.
		err := l.probe(ctx)
		if _, isLimited := limited(err); err != nil && !isLimited && !strings.Contains(err.Error(), "search.formats") {
			return fmt.Errorf("something answers at %s, and it is not a SearXNG: %w\nPick another port: ideacheck config set research.endpoints.searxng http://localhost:8888\nThen run this again.", l.Endpoint, err)
		}
		return l.report(err, "one already answers at "+l.Endpoint+" (not started by ideacheck)", step)
	case status != "":
		// Stopped, half-made, or on a port the config has since moved away from:
		// start over, with current settings and a new secret.
		if _, err := l.docker(ctx, nil, nil, "rm", "-f", "-v", l.Container); err != nil {
			return fmt.Errorf("remove the old %s: %w", l.Container, err)
		}
	}

	if _, err := l.docker(ctx, nil, nil, "image", "inspect", "--format", "{{.Id}}", l.Image); err != nil {
		step(Step{Label: "image", Text: "downloading " + l.Image + " (first time only)"})
		if _, err := l.docker(ctx, nil, nil, "pull", "-q", l.Image); err != nil {
			return fmt.Errorf("download %s: %w", l.Image, err)
		}
	}
	step(Step{Label: "image", Text: l.Image, Done: true})

	step(Step{Label: "searxng", Text: "starting " + l.Container})
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if _, err := l.docker(ctx, []string{"SEARXNG_SECRET=" + hex.EncodeToString(secret)}, nil,
		"create", "--name", l.Container, "--restart", "unless-stopped",
		"-p", "127.0.0.1:"+port+":8080", // this machine only: it has no rate limiter
		"-e", "SEARXNG_SECRET", l.Image); err != nil {
		return fmt.Errorf("create %s: %w", l.Container, err)
	}
	// Copied in, not mounted: the container then depends on no path of the
	// host's, and the binary needs no file on disk to hand over.
	if _, err := l.docker(ctx, nil, settingsTar(l.Settings), "cp", "-", l.Container+":/etc/searxng"); err != nil {
		l.remove(ctx)
		return fmt.Errorf("copy settings.yml into %s: %w", l.Container, err)
	}
	if _, err := l.docker(ctx, nil, nil, "start", l.Container); err != nil {
		l.remove(ctx)
		if said := strings.ToLower(err.Error()); strings.Contains(said, "already allocated") || strings.Contains(said, "address already in use") {
			return fmt.Errorf("port %s is taken by something that is not a SearXNG\nPick another: ideacheck config set research.endpoints.searxng http://localhost:8888\nThen run this again.", port)
		}
		return fmt.Errorf("start %s: %w", l.Container, err)
	}
	return l.report(l.answer(ctx), l.Endpoint, step)
}

// report closes Up: the row that says where it answers, and, when its engines
// are rate-limiting it right now, a warning rather than a failure — it is up.
func (l *Local) report(err error, where string, step func(Step)) error {
	engines, isLimited := limited(err)
	if err != nil && !isLimited {
		return err
	}
	step(Step{Label: "searxng", Text: where, Done: true})
	if isLimited {
		step(Step{Text: engines.Error(), Warn: true})
	}
	return nil
}

// Down removes the container; false means there was none.
func (l *Local) Down(ctx context.Context) (bool, error) {
	if _, err := l.Ready(ctx); err != nil {
		return false, err
	}
	if state, _ := l.status(ctx); state == "" {
		return false, nil
	}
	if _, err := l.docker(ctx, nil, nil, "rm", "-f", "-v", l.Container); err != nil {
		return false, err
	}
	return true, nil
}

// Status never fails: no Docker is a finding, and a SearXNG may answer at
// Endpoint without it.
func (l *Local) Status(ctx context.Context) State {
	var s State
	if s.Docker, s.DockerErr = l.Ready(ctx); s.DockerErr == nil {
		s.Container, _ = l.status(ctx)
	}
	if l.searx().Reachable(ctx) {
		s.AnswerErr = l.probe(ctx)
		_, isLimited := limited(s.AnswerErr)
		s.Answers = s.AnswerErr == nil || isLimited
	}
	return s
}

// answer waits for the container to come up, then proves a search works.
func (l *Local) answer(ctx context.Context) error {
	wait := l.Wait
	if wait == 0 {
		wait = time.Minute
	}
	deadline := time.Now().Add(wait)
	for !l.searx().Reachable(ctx) {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("%s started but never answered at %s\nSee why: docker logs %s", l.Container, l.Endpoint, l.Container)
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	return l.probe(ctx)
}

// probe is one real search: the only thing that shows JSON output is on.
func (l *Local) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := l.searx().Search(ctx, "ideacheck", 1)
	return err
}

// limited picks out the one probe failure that is not the instance's fault and
// not fixable here: it runs and takes JSON, and its engines are rate-limiting it.
func limited(err error) (*EnginesError, bool) {
	var engines *EnginesError
	return engines, errors.As(err, &engines)
}

// status is docker's word for the container's state ("" when there is none)
// and the host port it was created to publish, whatever state it is in.
func (l *Local) status(ctx context.Context) (state, port string) {
	out, err := l.docker(ctx, nil, nil, "inspect", "--format",
		`{{.State.Status}} {{range $p, $b := .HostConfig.PortBindings}}{{(index $b 0).HostPort}}{{end}}`, l.Container)
	if err != nil {
		return "", ""
	}
	state, port, _ = strings.Cut(out, " ")
	return state, port
}

func (l *Local) remove(ctx context.Context) {
	_, _ = l.docker(ctx, nil, nil, "rm", "-f", "-v", l.Container)
}

// port is the host port to publish, read off the endpoint research will query:
// one setting, so the two cannot disagree.
func (l *Local) port() (string, error) {
	u, err := url.Parse(l.Endpoint)
	local := err == nil && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")
	if !local {
		return "", fmt.Errorf("research.endpoints.searxng is %q, not a plain http://localhost address: there is nothing on this machine to start", l.Endpoint)
	}
	if u.Port() == "" {
		return "80", nil
	}
	return u.Port(), nil
}

func (l *Local) docker(ctx context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	if l.Docker != nil {
		return l.Docker(ctx, env, stdin, args...)
	}
	return DockerCLI(ctx, env, stdin, args...)
}

func (l *Local) searx() *SearX {
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &SearX{BaseURL: l.Endpoint, Client: client}
}

// settingsTar is settings.yml as the archive `docker cp -` reads from stdin.
func settingsTar(settings []byte) io.Reader {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	_ = w.WriteHeader(&tar.Header{Name: "settings.yml", Mode: 0o644, Size: int64(len(settings)), ModTime: time.Now()})
	_, _ = w.Write(settings)
	_ = w.Close()
	return &buf
}
