package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// TestUpgradeInstallsAndPrintsEveryStep drives the whole upgrade against a fake
// release: the binary must really be replaced, and the run must leave the same
// column of steps the install script prints, in the same order.
func TestUpgradeInstallsAndPrintsEveryStep(t *testing.T) {
	archive := releaseArchive(t, "NEW BINARY")
	name := fmt.Sprintf("ideacheck_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)

	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		case "/" + name:
			w.Write(archive)
		default:
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","html_url":"https://example.test/notes","assets":[
				{"name":%q,"browser_download_url":%q},
				{"name":"checksums.txt","browser_download_url":%q}]}`,
				name, base+"/"+name, base+"/checksums.txt")
		}
	}))
	defer srv.Close()
	base = srv.URL

	exe := filepath.Join(t.TempDir(), "ideacheck")
	if err := os.WriteFile(exe, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	a := &app{
		stdout: &out, stderr: &errb,
		environ:     func() []string { return nil },
		getenv:      func(string) string { return "" },
		home:        t.TempDir(),
		input:       osInputEnv(),
		releasesURL: srv.URL,
		executable:  func() (string, error) { return exe, nil },
	}
	if code := a.run(context.Background(), []string{"upgrade"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if b, _ := os.ReadFile(exe); string(b) != "NEW BINARY" {
		t.Fatalf("binary = %q; want the downloaded one", b)
	}
	// The steps, in order, each on its own line. Off a terminal there is no
	// spinner and no bar — one plain row per finished step, safe to grep.
	if i := strings.Index(out.String(), "\x1b"); i >= 0 {
		t.Errorf("escape codes in piped output at %d:\n%q", i, out.String())
	}
	var at int
	for _, want := range []string{"current", "latest     9.9.9", "download", "verify", "sha256 matches", "install", "ideacheck 9.9.9 is ready"} {
		i := strings.Index(out.String()[at:], want)
		if i < 0 {
			t.Fatalf("step %q missing or out of order in:\n%s", want, out.String())
		}
		at += i + len(want)
	}
}

// releaseArchive is a .tar.gz holding one ideacheck binary, like a real release.
func releaseArchive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "ideacheck", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
