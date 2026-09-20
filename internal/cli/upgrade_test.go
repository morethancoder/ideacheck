package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// releaseServer answers like the GitHub latest-release endpoint.
func releaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.test/notes"}`, tag)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runUpgradeCLI drives the real command tree against a fake release endpoint.
func runUpgradeCLI(t *testing.T, url string, tty bool, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errb,
		environ:     func() []string { return nil },
		getenv:      func(string) string { return "" },
		home:        t.TempDir(),
		input:       osInputEnv(),
		stdoutTTY:   tty,
		releasesURL: url,
	}
	code := a.run(context.Background(), args)
	return run{code, out.String(), errb.String()}
}

func TestUpgradeCheckReportsANewerRelease(t *testing.T) {
	srv := releaseServer(t, "v9.9.9")
	got := runUpgradeCLI(t, srv.URL, false, "upgrade", "--check")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	for _, want := range []string{"9.9.9 is available", "ideacheck upgrade", "https://example.test/notes"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout %q does not mention %q", got.stdout, want)
		}
	}
}

func TestUpgradeSaysNothingToDoOnTheLatest(t *testing.T) {
	// Version is "dev" in tests, so a release tagged v0.0.0-equal cannot be
	// matched; stamp the comparison by tagging the server with the same string.
	srv := releaseServer(t, "v"+Version)
	got := runUpgradeCLI(t, srv.URL, false, "upgrade", "--check")
	if !strings.Contains(got.stdout, "is the latest release") {
		t.Fatalf("stdout = %q; want the latest-release line", got.stdout)
	}
}

func TestUpgradeReportsARepoWithNoReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	got := runUpgradeCLI(t, srv.URL, false, "upgrade", "--check")
	if got.code == 0 || !strings.Contains(got.stderr, "no published releases") {
		t.Fatalf("exit %d, stderr %q; want a clear error", got.code, got.stderr)
	}
}

// TestNoUpdateNoticeOnADevBuild: the notice must never appear for a build that
// was not stamped from a tag, which is every build a developer runs locally.
func TestNoUpdateNoticeOnADevBuild(t *testing.T) {
	srv := releaseServer(t, "v9.9.9")
	got := runUpgradeCLI(t, srv.URL, true, "--version")
	if strings.Contains(got.stderr, "is available") {
		t.Fatalf("stderr = %q; want no notice on a dev build", got.stderr)
	}
}

func TestAboutVersions(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"upgrade"}, true},
		{[]string{"update"}, true},
		{[]string{"--version"}, true},
		{[]string{"-V"}, true},
		{[]string{"help", "serve"}, true},
		{[]string{"-q", "my idea"}, true},
		{[]string{"--quiet"}, true},
		{[]string{"my idea"}, false},
		{[]string{"-b", "mock", "my idea"}, false},
		// Idea text forced with -- is idea text, not a command.
		{[]string{"--", "upgrade"}, false},
		// "upgrade my product" is an idea, not the upgrade command.
		{[]string{"upgrade my product onboarding"}, false},
	}
	for _, c := range cases {
		if got := aboutVersions(c.args); got != c.want {
			t.Errorf("aboutVersions(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
