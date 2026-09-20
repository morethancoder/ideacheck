package selfupdate

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
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.1.0", "v0.1.1", true},
		{"0.9.0", "1.0.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0", "0.1.9", false},
		{"1.0.0", "0.99.99", false},
		// Builds that did not come from a tag are the developer's own; a
		// background check must never nag about those.
		{"dev", "0.2.0", false},
		{"v0.1.0-3-gab12cd", "0.2.0", false},
		{"v0.1.0-dirty", "0.2.0", false},
		// Nor should anyone be moved onto a pre-release by a background check.
		{"0.1.0", "0.2.0-rc1", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/none" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v0.3.0","html_url":"https://example.test/r/0.3.0",
			"assets":[{"name":"ideacheck_0.3.0_darwin_arm64.tar.gz","browser_download_url":"https://example.test/a"}]}`)
	}))
	defer srv.Close()

	rel, err := Latest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.3.0" || rel.Page != "https://example.test/r/0.3.0" || len(rel.Assets) != 1 {
		t.Fatalf("got %+v", rel)
	}

	// A repository with no releases must read as an error, never as "you are
	// up to date" and never as an upgrade.
	if _, err := Latest(context.Background(), srv.Client(), srv.URL+"/none"); err == nil {
		t.Fatal("want an error for a repo with no releases")
	}
}

func TestInstall(t *testing.T) {
	archive := tarGz(t, "ideacheck", "NEW BINARY")
	name := fmt.Sprintf("ideacheck_0.3.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)

	var served string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + sumsFile:
			fmt.Fprintf(w, "%s  %s\n", served, name)
		default:
			w.Write(archive)
		}
	}))
	defer srv.Close()

	rel := Release{Version: "0.3.0", Assets: []Asset{
		{Name: name, URL: srv.URL + "/" + name},
		{Name: sumsFile, URL: srv.URL + "/" + sumsFile},
	}}
	exe := filepath.Join(t.TempDir(), "ideacheck")
	if err := os.WriteFile(exe, []byte("OLD BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A checksum that does not match must stop the install with the old binary
	// still in place: this is the only thing standing between a download and
	// the file the user executes.
	served = strings.Repeat("0", 64)
	if err := Install(context.Background(), srv.Client(), rel, exe, nil); err == nil {
		t.Fatal("want an error when the checksum does not match")
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD BINARY" {
		t.Fatalf("a failed install replaced the binary: %q", b)
	}

	served = hex.EncodeToString(sum[:])
	if err := Install(context.Background(), srv.Client(), rel, exe, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil || string(b) != "NEW BINARY" {
		t.Fatalf("binary = %q, %v", b, err)
	}
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v; want an executable", info.Mode().Perm(), err)
	}
}

func TestInstallRefusesWithoutChecksums(t *testing.T) {
	rel := Release{Version: "0.3.0", Assets: []Asset{
		{Name: fmt.Sprintf("ideacheck_0.3.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH), URL: "https://example.test/a"},
	}}
	err := Install(context.Background(), http.DefaultClient, rel, filepath.Join(t.TempDir(), "ideacheck"), nil)
	if err == nil || !strings.Contains(err.Error(), sumsFile) {
		t.Fatalf("err = %v; want a refusal naming %s", err, sumsFile)
	}
}

func TestManager(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	none := env(nil)
	cases := []struct {
		exe     string
		getenv  func(string) string
		want    string
		command string
	}{
		{"/opt/homebrew/Cellar/ideacheck/0.1.0/bin/ideacheck", none, "Homebrew", "brew upgrade ideacheck"},
		{"/Users/x/go/bin/ideacheck", none, "go install", "go install " + Module + "/cmd/ideacheck@latest"},
		{"/tmp/gb/ideacheck", env(map[string]string{"GOBIN": "/tmp/gb"}), "go install", "go install " + Module + "/cmd/ideacheck@latest"},
		{"/Users/x/.local/bin/ideacheck", none, "", ""},
		{"/usr/local/bin/ideacheck", none, "", ""},
	}
	for _, c := range cases {
		manager, command := Manager(c.exe, c.getenv)
		if manager != c.want || command != c.command {
			t.Errorf("Manager(%q) = %q, %q; want %q, %q", c.exe, manager, command, c.want, c.command)
		}
	}
}

func TestCheckerUsesCacheUntilItIsStale(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		fmt.Fprint(w, `{"tag_name":"v0.3.0","html_url":"https://example.test/r"}`)
	}))
	defer srv.Close()

	now := time.Now()
	c := Checker{
		Dir: t.TempDir(), Current: "0.1.0", URL: srv.URL, Client: srv.Client(),
		Now: func() time.Time { return now }, Every: time.Hour, Wait: 5 * time.Second,
	}

	found := c.Start(context.Background())()
	if found == nil || found.Version != "0.3.0" {
		t.Fatalf("found = %+v; want 0.3.0", found)
	}
	// Within the window the remembered answer is used, and GitHub is not asked
	// again — that is the whole point of the cache.
	if got := c.Start(context.Background())(); got == nil || got.Version != "0.3.0" {
		t.Fatalf("second run = %+v; want the cached 0.3.0", got)
	}
	if hits != 1 {
		t.Fatalf("asked GitHub %d times; want 1", hits)
	}
	// Past the window it asks again.
	now = now.Add(2 * time.Hour)
	c.Now = func() time.Time { return now }
	c.Start(context.Background())()
	if hits != 2 {
		t.Fatalf("asked GitHub %d times after the window; want 2", hits)
	}
}

func TestCheckerSaysNothingWhenCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.3.0"}`)
	}))
	defer srv.Close()
	c := Checker{Dir: t.TempDir(), Current: "0.3.0", URL: srv.URL, Client: srv.Client(), Wait: 5 * time.Second}
	if found := c.Start(context.Background())(); found != nil {
		t.Fatalf("found = %+v; want nothing on the latest version", found)
	}
}

// TestCheckerSurvivesAnUnreachableGitHub: being offline is normal, and must
// cost the user nothing but the wait.
func TestCheckerSurvivesAnUnreachableGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := Checker{Dir: t.TempDir(), Current: "0.1.0", URL: srv.URL, Client: srv.Client(), Wait: 5 * time.Second}
	if found := c.Start(context.Background())(); found != nil {
		t.Fatalf("found = %+v; want nothing when the lookup fails", found)
	}
}

func tarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
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
