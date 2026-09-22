// Package selfupdate keeps an installed ideacheck current. It asks GitHub for
// the newest release, decides whether that release is actually newer than the
// running binary, and can replace that binary with a checksum-verified build.
//
// Two rules shape everything here. A check never blocks a run: the lookup is a
// background goroutine whose answer is read once the command has finished, and
// is cached for a day. And a download is never trusted: the archive is checked
// against the release's checksums.txt before it is allowed anywhere near the
// binary on disk.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	// Module is the Go module path, which is also the repository URL.
	Module = "github.com/morethancoder/ideacheck"
	// Repo is the GitHub repository in owner/name form.
	Repo = "morethancoder/ideacheck"
	// LatestURL is the API endpoint for the newest published release.
	LatestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

	// sumsFile is the checksum manifest goreleaser publishes with every release.
	sumsFile = "checksums.txt"
	// maxJSON caps the release JSON; the real thing is a few kilobytes.
	maxJSON = 1 << 20
)

// Release is the part of a GitHub release this tool acts on.
type Release struct {
	Version string   // the tag without its leading "v", e.g. "0.2.0"
	Page    string   // the release page, shown so the user can read the notes
	Notes   []string // the headline of each change, one line each; see Notes
	Assets  []Asset  // published files, in the order GitHub lists them
}

// Asset is one published file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Latest asks GitHub for the newest published release. url is for tests; empty
// means LatestURL. A repository with no releases yet answers 404, which comes
// back as an error rather than as a version — the caller must not read "no
// releases" as "you are out of date".
func Latest(ctx context.Context, client *http.Client, url string) (Release, error) {
	if url == "" {
		url = LatestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("asking GitHub for the latest release: %w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("%s has no published releases yet", Repo)
	default:
		return Release{}, fmt.Errorf("GitHub answered HTTP %d for the latest release", res.StatusCode)
	}
	var body struct {
		TagName string  `json:"tag_name"`
		HTMLURL string  `json:"html_url"`
		Body    string  `json:"body"`
		Assets  []Asset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxJSON)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading the release from GitHub: %w", err)
	}
	if body.TagName == "" {
		return Release{}, fmt.Errorf("the latest release of %s has no tag", Repo)
	}
	return Release{Version: Clean(body.TagName), Page: body.HTMLURL, Notes: Notes(body.Body), Assets: body.Assets}, nil
}

// Notes pulls the headline of each change out of a release's Markdown body, so
// an upgrade can say what it brought without sending the user to a web page.
//
// goreleaser writes the body as a "## Changelog" section of bullets, one commit
// subject each, prefixed with the commit hash, followed by the footer from
// .goreleaser.yaml (the install snippet). Only that section's bullets count when
// it exists; a hand-written body with no such heading contributes every bullet
// it has. The hash and a trailing PR number are dropped — they are for git, not
// for someone deciding whether to read on. Anything inside a code fence is
// skipped, so a "* " in a shell example is not a change.
func Notes(body string) []string {
	var notes []string
	inChangelog, sawChangelog, fenced := false, false, false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "```"):
			fenced = !fenced
			continue
		case fenced:
			continue
		case strings.HasPrefix(line, "#"):
			title := strings.ToLower(strings.TrimLeft(line, "# "))
			inChangelog = strings.Contains(title, "changelog") || strings.Contains(title, "what's changed")
			if inChangelog {
				sawChangelog = true
				notes = notes[:0] // bullets before the heading were preamble
			}
			continue
		}
		if sawChangelog && !inChangelog {
			continue
		}
		if note, ok := bullet(line); ok {
			notes = append(notes, note)
		}
	}
	return notes
}

// bullet reads one Markdown list item as a note, minus the commit hash and PR
// reference goreleaser and GitHub decorate it with.
func bullet(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "* ")
	if !ok {
		if rest, ok = strings.CutPrefix(line, "- "); !ok {
			return "", false
		}
	}
	if head, tail, _ := strings.Cut(rest, " "); isHash(head) {
		rest = tail
	}
	if i := strings.LastIndex(rest, " (#"); i > 0 && strings.HasSuffix(rest, ")") {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)
	return rest, rest != ""
}

// isHash reports whether s looks like an abbreviated or full git commit hash.
func isHash(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// asset finds the archive built for one machine. Release files are named
// ideacheck_<version>_<os>_<arch>.tar.gz, but matching on the os/arch pair
// rather than on the whole name keeps this working if that ever changes.
func (r Release) asset(goos, goarch string) (Asset, error) {
	for _, a := range r.Assets {
		name := strings.ToLower(a.Name)
		if strings.HasSuffix(name, ".tar.gz") && strings.Contains(name, goos) && strings.Contains(name, goarch) {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s has no build for %s/%s", r.Version, goos, goarch)
}

// find returns the named asset of the release.
func (r Release) find(name string) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s does not publish %s", r.Version, name)
}

// Clean strips the leading "v" a tag carries but a version string does not.
func Clean(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "v") }

// Newer reports whether latest is a higher version than current.
//
// Only plain X.Y.Z versions count. A build that was not stamped from a tag —
// "dev", or a `git describe` string such as "v0.1.0-3-gab12cd-dirty" — is a
// developer's own build, and telling them their working copy is out of date
// would be noise. A pre-release tag is skipped for the same reason: nobody is
// moved onto a release candidate by a background check.
func Newer(current, latest string) bool {
	c, okc := parse(current)
	l, okl := parse(latest)
	return okc && okl && l.after(c)
}

// Released reports whether s is a stamped release version, i.e. whether this
// binary is one a background check should ever nag about.
func Released(s string) bool { _, ok := parse(s); return ok }

type version [3]int

func parse(s string) (version, bool) {
	parts := strings.Split(Clean(s), ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v[i] = n
	}
	return v, true
}

func (v version) after(o version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] > o[i]
		}
	}
	return false
}
