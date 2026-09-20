package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// binName is the file inside the release archive, and the name of the command.
const binName = "ideacheck"

// maxArchive caps a download. A release archive is a few megabytes; this is only
// here so a wrong URL cannot fill the disk.
const maxArchive = 256 << 20

// Install replaces exe with the build of rel for this machine.
//
// The archive is verified against the release's checksums.txt before it is
// unpacked, and the new binary is written beside the old one and renamed over
// it. A rename is atomic, so an interrupted upgrade leaves the working
// ideacheck in place rather than half a new one.
func Install(ctx context.Context, client *http.Client, rel Release, exe string) error {
	archive, err := rel.asset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	sums, err := rel.find(sumsFile)
	if err != nil {
		// Without checksums there is nothing to verify the download against,
		// and an unverified binary is not worth the convenience.
		return fmt.Errorf("%w — refusing to install a download that cannot be verified", err)
	}
	manifest, err := download(ctx, client, sums.URL)
	if err != nil {
		return err
	}
	want, err := checksum(string(manifest), archive.Name)
	if err != nil {
		return err
	}
	blob, err := download(ctx, client, archive.URL)
	if err != nil {
		return err
	}
	if got := sha256.Sum256(blob); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s does not match its checksum in %s — the download was corrupted or tampered with", archive.Name, sumsFile)
	}
	bin, err := unpack(blob)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

// Manager names the package manager that owns exe, when one does, along with
// the command that upgrades it. Replacing a binary another tool installed would
// be quietly undone by that tool's next upgrade, so those installs are pointed
// at their own command instead of being overwritten.
func Manager(exe string, getenv func(string) string) (manager, command string) {
	p := filepath.ToSlash(exe)
	dir := path.Dir(p)
	switch {
	case strings.Contains(p, "/Cellar/"), strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return "Homebrew", "brew upgrade " + binName
	case dir == gobin(getenv), strings.HasSuffix(dir, "/go/bin"):
		return "go install", "go install " + Module + "/cmd/" + binName + "@latest"
	}
	return "", ""
}

// gobin is where `go install` puts binaries: GOBIN, else GOPATH/bin.
func gobin(getenv func(string) string) string {
	if b := getenv("GOBIN"); b != "" {
		return strings.TrimSuffix(filepath.ToSlash(b), "/")
	}
	if p := getenv("GOPATH"); p != "" {
		return strings.TrimSuffix(filepath.ToSlash(p), "/") + "/bin"
	}
	return ""
}

// download reads a release file into memory. Releases are small enough that
// streaming to disk would buy nothing and cost a temp file to clean up.
func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", path.Base(url), err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %d", path.Base(url), res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxArchive))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", path.Base(url), err)
	}
	return b, nil
}

// checksum reads one "<sha256>  <file>" line out of a checksums manifest.
func checksum(manifest, name string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		sum, file, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(file), "*")) == name {
			return sum, nil
		}
	}
	return "", fmt.Errorf("%s has no checksum for %s", sumsFile, name)
}

// unpack pulls the ideacheck binary out of a .tar.gz release archive.
func unpack(blob []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("reading the release archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the release archive holds no %s binary", binName)
		}
		if err != nil {
			return nil, fmt.Errorf("reading the release archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != binName {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, maxArchive))
		if err != nil {
			return nil, fmt.Errorf("reading %s out of the archive: %w", binName, err)
		}
		return bin, nil
	}
}

// replace writes bin over exe through a temp file in the same directory, so the
// final step is an atomic rename rather than a truncate-and-write.
func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, "."+binName+"-*")
	if err != nil {
		return notWritable(dir, err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename below has succeeded
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return notWritable(dir, err)
	}
	return nil
}

// notWritable turns a permission error into the sentence that fixes it.
func notWritable(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s is not writable by you, so ideacheck cannot replace itself there. Either run `sudo ideacheck upgrade`, or reinstall somewhere you own with: curl -fsSL https://raw.githubusercontent.com/"+Repo+"/main/install.sh | sh", dir)
	}
	return err
}
