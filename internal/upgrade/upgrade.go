// Package upgrade installs checksum-verified binaries from Flightbay releases.
package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const repository = "tjorourke/flightbay"

var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// Options controls which release to install. By default an installed sibling
// flightbay-desk is upgraded too, so the collector and desk stay together.
type Options struct {
	Version       string
	Check         bool
	CollectorOnly bool
}

// Run replaces the executable behind any symlink, preserving the link itself.
func Run(ctx context.Context, current string, opts Options, out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u := updater{client: &http.Client{Timeout: 5 * time.Minute},
		api:       "https://api.github.com/repos/" + repository,
		downloads: "https://github.com/" + repository + "/releases/download",
		goos:      runtime.GOOS, arch: runtime.GOARCH, exe: exe, out: out}
	return u.run(ctx, current, opts)
}

type updater struct {
	client                     *http.Client
	api, downloads, goos, arch string
	exe                        string
	out                        io.Writer
}

type target struct {
	name, path, staged, backup string
}

func (u updater) run(ctx context.Context, current string, opts Options) error {
	if u.goos != "darwin" && u.goos != "linux" {
		return fmt.Errorf("in-place upgrades support macOS and Linux; download the Windows ZIP from https://github.com/%s/releases/latest", repository)
	}
	if u.arch != "arm64" && u.arch != "amd64" {
		return fmt.Errorf("no release binary for %s/%s", u.goos, u.arch)
	}
	exe, err := filepath.EvalSymlinks(u.exe)
	if err != nil {
		return err
	}
	if strings.Contains(filepath.ToSlash(exe), "/Cellar/") || strings.Contains(filepath.ToSlash(exe), "/Caskroom/") {
		return errors.New("this installation is managed by Homebrew; run: brew update && brew upgrade flightbay")
	}
	tag := opts.Version
	if tag != "" && !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	endpoint := u.api + "/releases/latest"
	if tag != "" {
		if !releaseTag.MatchString(tag) {
			return fmt.Errorf("version must be a stable release such as v0.2.5")
		}
		endpoint = u.api + "/releases/tags/" + tag
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	b, err := u.fetch(ctx, endpoint, 2<<20)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return fmt.Errorf("read release: %w", err)
	}
	if !releaseTag.MatchString(rel.Tag) || rel.Draft || rel.Prerelease || (tag != "" && rel.Tag != tag) {
		return errors.New("GitHub did not return the requested published stable release")
	}
	fmt.Fprintf(u.out, "Installed collector: %s\nRelease: %s\n", current, rel.Tag)
	if opts.Check {
		return nil
	}
	targets := []target{{name: "flightbay", path: exe}}
	if !opts.CollectorOnly {
		p := filepath.Join(filepath.Dir(exe), "flightbay-desk")
		if _, err := os.Lstat(p); err == nil {
			p, err = filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
			if strings.Contains(filepath.ToSlash(p), "/Cellar/") || strings.Contains(filepath.ToSlash(p), "/Caskroom/") {
				return errors.New("the sibling desk is managed by Homebrew; upgrade it with brew, or use --collector-only")
			}
			targets = append(targets, target{name: "flightbay-desk", path: p})
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// Stage every binary and a backup before replacing any installed file.
	defer func() {
		for _, t := range targets {
			if t.staged != "" {
				_ = os.Remove(t.staged)
			}
			if t.backup != "" {
				_ = os.Remove(t.backup)
			}
		}
	}()
	base := u.downloads + "/" + rel.Tag + "/"
	checksums, err := u.fetch(ctx, base+"checksums-flightbay.txt", 1<<20)
	if err != nil {
		return err
	}
	for i := range targets {
		t := &targets[i]
		archive := fmt.Sprintf("%s_%s_%s_%s.tar.gz", t.name, strings.TrimPrefix(rel.Tag, "v"), u.goos, u.arch)
		sums := checksums
		if t.name == "flightbay-desk" {
			sums, err = u.fetch(ctx, base+archive+".sha256", 1<<20)
			if err != nil {
				return err
			}
		}
		want, err := checksum(sums, archive)
		if err != nil {
			return err
		}
		fmt.Fprintf(u.out, "Downloading %s\n", archive)
		t.staged, err = u.stage(ctx, base+archive, want, *t)
		if err != nil {
			return err
		}
		t.backup, err = backup(t.path)
		if err != nil {
			return err
		}
	}
	for i, t := range targets {
		if err := os.Rename(t.staged, t.path); err != nil {
			var restoreErr error
			for j := 0; j < i; j++ {
				if e := os.Rename(targets[j].backup, targets[j].path); e != nil {
					restoreErr = errors.Join(restoreErr, fmt.Errorf("restore %s manually from %s: %w", targets[j].path, targets[j].backup, e))
					targets[j].backup = "" // Keep the recovery copy if restoration failed.
				}
			}
			return errors.Join(fmt.Errorf("replace %s: %w", t.path, err), restoreErr)
		}
	}
	for _, t := range targets {
		fmt.Fprintf(u.out, "Updated %s to %s at %s\n", t.name, rel.Tag, t.path)
	}
	fmt.Fprintln(u.out, "Restart any running desk to use the new version.")
	return nil
}

func (u updater) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	r, err := u.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes: %s", limit, url)
	}
	return b, err
}

func (u updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "flightbay-upgrade")
	r, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != http.StatusOK {
		r.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, r.Status)
	}
	return r, nil
}

func checksum(data []byte, name string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			b, err := hex.DecodeString(f[0])
			if err == nil && len(b) == sha256.Size {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	return "", fmt.Errorf("no valid SHA-256 checksum for %s", name)
}

func (u updater) stage(ctx context.Context, url, want string, t target) (string, error) {
	archive, err := os.CreateTemp("", "flightbay-download-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	r, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	const maxArchive = 256 << 20
	n, err := io.Copy(io.MultiWriter(archive, h), io.LimitReader(r.Body, maxArchive+1))
	r.Body.Close()
	if err != nil {
		return "", err
	}
	if n > maxArchive || hex.EncodeToString(h.Sum(nil)) != want {
		return "", fmt.Errorf("checksum verification failed for %s; installed binaries were not changed", t.name)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var staged string
	ok := false
	defer func() {
		if !ok && staged != "" {
			_ = os.Remove(staged)
		}
	}()
	for {
		head, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if head.Name != t.name && head.Name != "./"+t.name {
			continue
		}
		if staged != "" || head.Typeflag != tar.TypeReg || head.Size <= 0 || head.Size > 256<<20 {
			return "", fmt.Errorf("invalid binary entry in %s archive", t.name)
		}
		f, err := os.CreateTemp(filepath.Dir(t.path), ".flightbay-upgrade-*")
		if err != nil {
			return "", fmt.Errorf("cannot write installation directory %s: %w", filepath.Dir(t.path), err)
		}
		staged = f.Name()
		_, err = io.CopyN(f, tr, head.Size)
		if err == nil {
			err = f.Chmod(0o755)
		}
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return "", err
		}
	}
	if staged == "" {
		return "", fmt.Errorf("release archive has no %s binary", t.name)
	}
	ok = true
	return staged, nil
}

func backup(path string) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".flightbay-previous-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, src)
	if err == nil {
		err = f.Chmod(st.Mode().Perm())
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
