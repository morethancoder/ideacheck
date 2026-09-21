package search

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDocker answers like the docker CLI for the handful of commands Local
// runs, and records them. `start` is what brings the SearXNG behind it up.
type fakeDocker struct {
	status   string // of the container; "" = none
	port     string // the host port it publishes; "" = whatever is asked for
	hasImage bool
	startErr error
	up       *atomic.Bool // the fake SearXNG answers once true

	calls    []string
	env      []string
	settings string // settings.yml as copied in
}

func (f *fakeDocker) run(_ context.Context, env []string, stdin io.Reader, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "version":
		return "28.3.0", nil
	case "inspect":
		if f.status == "" {
			return "", errors.New("Error: No such object: " + args[len(args)-1])
		}
		return f.status + " " + f.port, nil
	case "image":
		if !f.hasImage {
			return "", errors.New("Error: No such image")
		}
	case "create":
		f.env, f.status = env, "created"
	case "cp":
		r := tar.NewReader(stdin)
		if h, err := r.Next(); err == nil && h.Name == "settings.yml" {
			b, _ := io.ReadAll(r)
			f.settings = string(b)
		}
	case "start":
		if f.startErr != nil {
			return "", f.startErr
		}
		f.status = "running"
		f.up.Store(true)
	case "rm":
		f.status = ""
		f.up.Store(false)
	}
	return "", nil
}

func (f *fakeDocker) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// searxng is a fake SearXNG that is down until up is set; json says whether
// it has JSON output on.
func searxng(t *testing.T, up *atomic.Bool, json bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !up.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/healthz":
			io.WriteString(w, "OK")
		case !json:
			w.WriteHeader(http.StatusForbidden)
		default:
			io.WriteString(w, `{"results":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func portOf(endpoint string) string {
	u, _ := url.Parse(endpoint)
	return u.Port()
}

func local(f *fakeDocker, endpoint string) *Local {
	return &Local{Image: "searxng/searxng:latest", Container: "ideacheck-searxng", Endpoint: endpoint,
		Settings: []byte("search:\n  formats: [html, json]\n"), Docker: f.run, Wait: 2 * time.Second}
}

func TestUpStartsAContainerWhereResearchWillLook(t *testing.T) {
	f := &fakeDocker{up: &atomic.Bool{}}
	srv := searxng(t, f.up, true)
	var steps []string
	if err := local(f, srv.URL).Up(context.Background(), func(s Step) {
		if s.Done {
			steps = append(steps, s.Label)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(steps, " "); got != "docker image searxng" {
		t.Errorf("finished steps = %q", got)
	}
	if !f.ran("pull -q searxng/searxng:latest") {
		t.Errorf("an image that is not there must be downloaded: %v", f.calls)
	}
	u, _ := url.Parse(srv.URL)
	var create string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "create ") {
			create = c
		}
	}
	// This machine only, on the port of the endpoint research queries.
	if !strings.Contains(create, "-p 127.0.0.1:"+u.Port()+":8080") {
		t.Errorf("create = %q", create)
	}
	// The secret travels in the environment, never as an argument.
	if len(f.env) != 1 || !strings.HasPrefix(f.env[0], "SEARXNG_SECRET=") || len(f.env[0]) < 40 || strings.Contains(create, strings.TrimPrefix(f.env[0], "SEARXNG_SECRET=")) {
		t.Errorf("secret: env %v, create %q", f.env, create)
	}
	if !strings.Contains(f.settings, "json") {
		t.Errorf("settings.yml copied in = %q", f.settings)
	}
}

func TestUpLeavesARunningOneAlone(t *testing.T) {
	f := &fakeDocker{status: "running", up: &atomic.Bool{}}
	f.up.Store(true)
	srv := searxng(t, f.up, true)
	f.port = portOf(srv.URL)
	if err := local(f, srv.URL).Up(context.Background(), func(Step) {}); err != nil {
		t.Fatal(err)
	}
	if f.ran("create") || f.ran("rm") {
		t.Errorf("calls = %v", f.calls)
	}
}

// research.endpoints.searxng moved since the container was made: the running
// one publishes the old port, and waiting for it on the new one would never end.
func TestUpRemakesOneThatPublishesAnotherPort(t *testing.T) {
	f := &fakeDocker{status: "running", port: "1", hasImage: true, up: &atomic.Bool{}}
	srv := searxng(t, f.up, true)
	if err := local(f, srv.URL).Up(context.Background(), func(Step) {}); err != nil {
		t.Fatal(err)
	}
	if !f.ran("rm -f -v ideacheck-searxng") || !f.ran("create") {
		t.Errorf("calls = %v", f.calls)
	}
}

// Whatever holds the port answers /healthz and is not a SearXNG.
func TestUpSaysWhenSomethingElseHoldsThePort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>hello</html>") }))
	t.Cleanup(srv.Close)
	f := &fakeDocker{up: &atomic.Bool{}}
	err := local(f, srv.URL).Up(context.Background(), func(Step) {})
	if err == nil || !strings.Contains(err.Error(), "not a SearXNG") || !strings.Contains(err.Error(), "config set research.endpoints.searxng") || f.ran("create") {
		t.Errorf("err = %v, calls = %v", err, f.calls)
	}
}

func TestUpReplacesAStoppedOne(t *testing.T) {
	f := &fakeDocker{status: "exited", hasImage: true, up: &atomic.Bool{}}
	srv := searxng(t, f.up, true)
	if err := local(f, srv.URL).Up(context.Background(), func(Step) {}); err != nil {
		t.Fatal(err)
	}
	if !f.ran("rm -f -v ideacheck-searxng") || !f.ran("create") || f.ran("pull") {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestUpUsesASearXNGSomebodyElseRuns(t *testing.T) {
	f := &fakeDocker{up: &atomic.Bool{}}
	f.up.Store(true)
	if err := local(f, searxng(t, f.up, true).URL).Up(context.Background(), func(Step) {}); err != nil {
		t.Fatal(err)
	}
	if f.ran("create") {
		t.Errorf("a second one on the same port cannot work: %v", f.calls)
	}
	// One that refuses JSON is no use, and the error says what to change.
	err := local(f, searxng(t, f.up, false).URL).Up(context.Background(), func(Step) {})
	if err == nil || !strings.Contains(err.Error(), "search.formats") {
		t.Errorf("err = %v", err)
	}
}

func TestUpSaysWhatToDoAboutATakenPort(t *testing.T) {
	f := &fakeDocker{hasImage: true, up: &atomic.Bool{}, startErr: errors.New("Error response from daemon: Bind for 127.0.0.1:8080 failed: port is already allocated")}
	srv := searxng(t, f.up, true)
	err := local(f, srv.URL).Up(context.Background(), func(Step) {})
	if err == nil || !strings.Contains(err.Error(), "config set research.endpoints.searxng") {
		t.Fatalf("err = %v", err)
	}
	if f.status != "" {
		t.Errorf("a container that could not start is left behind (%s)", f.status)
	}
}

func TestUpRefusesAnEndpointThatIsNotThisMachine(t *testing.T) {
	for _, endpoint := range []string{"https://searx.example.org", "https://localhost:8080", ""} {
		f := &fakeDocker{up: &atomic.Bool{}}
		if err := local(f, endpoint).Up(context.Background(), func(Step) {}); err == nil || len(f.calls) != 0 {
			t.Errorf("%q: err = %v, calls = %v", endpoint, err, f.calls)
		}
	}
}

func TestReadySaysHowToGetDockerGoing(t *testing.T) {
	cases := []struct {
		name, goos string
		err        error
		problem    string
		advice     []string
	}{
		{"missing on a mac", "darwin", fmt.Errorf("exec: %w", exec.ErrNotFound), "not installed", []string{"brew install --cask docker", "TAVILY_API_KEY"}},
		{"missing on linux", "linux", fmt.Errorf("exec: %w", exec.ErrNotFound), "not installed", []string{"get.docker.com"}},
		{"missing on something else", "freebsd", fmt.Errorf("exec: %w", exec.ErrNotFound), "not installed", []string{"docs.docker.com/engine/install"}},
		{"stopped on a mac", "darwin", errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"), "not running", []string{"open -a Docker"}},
		{"stopped on linux", "linux", errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"), "not running", []string{"systemctl start docker"}},
		{"stopped on windows", "windows", errors.New(`error during connect: Get "http://%2F%2F.%2Fpipe%2Fdocker_engine/v1.47/version": open //./pipe/docker_engine: The system cannot find the file specified.`), "not running", []string{"Start menu"}},
		{"not allowed", "linux", errors.New("permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock"), "may not use it", []string{"usermod -aG docker"}},
	}
	for _, c := range cases {
		l := &Local{GOOS: c.goos, Docker: func(context.Context, []string, io.Reader, ...string) (string, error) { return "", c.err }}
		_, err := l.Ready(context.Background())
		var de *DockerError
		if !errors.As(err, &de) || !strings.Contains(de.Problem, c.problem) {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		for _, want := range c.advice {
			if !strings.Contains(de.Advice, want) {
				t.Errorf("%s: advice %q lacks %q", c.name, de.Advice, want)
			}
		}
	}
}

func TestStatusWorksWithoutDocker(t *testing.T) {
	up := &atomic.Bool{}
	up.Store(true)
	l := &Local{Endpoint: searxng(t, up, true).URL, Docker: func(context.Context, []string, io.Reader, ...string) (string, error) {
		return "", fmt.Errorf("exec: %w", exec.ErrNotFound)
	}}
	if s := l.Status(context.Background()); s.DockerErr == nil || !s.Answers {
		t.Errorf("state = %+v", s)
	}
}

// Rate-limited engines are not a failed start: the instance is up and takes
// JSON. Up says so and warns; nothing about the container is wrong.
func TestUpWarnsWhenTheEnginesAreRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			io.WriteString(w, "OK")
			return
		}
		io.WriteString(w, `{"results":[],"unresponsive_engines":[["brave","Suspended: too many requests"]]}`)
	}))
	t.Cleanup(srv.Close)
	f := &fakeDocker{status: "running", port: portOf(srv.URL), up: &atomic.Bool{}}
	var warned bool
	if err := local(f, srv.URL).Up(context.Background(), func(s Step) { warned = warned || (s.Warn && strings.Contains(s.Text, "brave")) }); err != nil || !warned {
		t.Errorf("err = %v, warned = %v", err, warned)
	}
	if s := local(f, srv.URL).Status(context.Background()); !s.Answers || s.AnswerErr == nil {
		t.Errorf("state = %+v", s)
	}
}
