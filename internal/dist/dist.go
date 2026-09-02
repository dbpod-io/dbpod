// Package dist manages install-free engine distributions: download, verify,
// extract and cache engine binaries under DBPOD_HOME/versions.
package dist

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shapled/dbpod/internal/metadata"
	"github.com/shapled/dbpod/internal/project"
)

// PackageRef identifies an engine@version spec from the CLI (e.g. "mysql@8.0.35").
type PackageRef struct {
	Engine  string
	Version string
}

// ParseRef parses "mysql@8.0.35" (series like "mysql@8.0" allowed).
func ParseRef(s string) (PackageRef, error) {
	engine, version, ok := strings.Cut(s, "@")
	if !ok || engine == "" || version == "" {
		return PackageRef{}, fmt.Errorf("invalid engine spec %q, want <engine>@<version> (e.g. mysql@8.0.35)", s)
	}
	return PackageRef{Engine: engine, Version: version}, nil
}

func (r PackageRef) String() string { return r.Engine + "@" + r.Version }

// Installed reports whether the engine@version is extracted locally.
func Installed(engine, version string) bool {
	root, err := imageRoot(engine, version)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(root, ".dbpod-root"))
	return err == nil
}

// imageRoot is the platform-specific extraction directory.
func imageRoot(engine, version string) (string, error) {
	return project.ImageDir(engine, version)
}

// root resolves the directory that actually contains bin/, honoring the
// .dbpod-root marker written after extraction (archives nest a top-level dir).
func root(engine, version string) (string, error) {
	base, err := imageRoot(engine, version)
	if err != nil {
		return "", err
	}
	markerPath := filepath.Join(base, ".dbpod-root")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return "", fmt.Errorf("engine %s is not installed", engine+"@"+version)
	}
	rel := strings.TrimSpace(string(data))
	if rel == "." {
		return base, nil
	}
	return filepath.Join(base, rel), nil
}

// BinaryPath returns the absolute path of a binary (e.g. "mysqld") inside an
// installed engine distribution.
func BinaryPath(engine, version, bin string) (string, error) {
	r, err := root(engine, version)
	if err != nil {
		return "", err
	}
	p := filepath.Join(r, "bin", bin)
	if runtime.GOOS == "windows" && filepath.Ext(p) == "" {
		p += ".exe"
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("binary %s not found in %s", bin, r)
	}
	return p, nil
}

// ListLocal returns all installed engine@version refs sorted by name.
func ListLocal() ([]PackageRef, error) {
	vdir, err := project.VersionsDir()
	if err != nil {
		return nil, err
	}
	var out []PackageRef
	engines, err := os.ReadDir(vdir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range engines {
		if !e.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(vdir, e.Name()))
		if err != nil {
			continue
		}
		for _, v := range versions {
			if v.IsDir() && Installed(e.Name(), v.Name()) {
				out = append(out, PackageRef{Engine: e.Name(), Version: v.Name()})
			}
		}
	}
	return out, nil
}

// Remove deletes the cached distribution of engine@version.
func Remove(engine, version string) error {
	if !Installed(engine, version) {
		return fmt.Errorf("engine %s is not installed", engine+"@"+version)
	}
	base, err := imageRoot(engine, version)
	if err != nil {
		return err
	}
	return os.RemoveAll(base)
}

// Path returns the installation directory of an installed distribution
// ("" when not installed).
func Path(engine, version string) string {
	r, err := root(engine, version)
	if err != nil {
		return ""
	}
	return r
}

// HasBinary reports whether the named binary exists in an installed
// distribution.
func HasBinary(engine, version, bin string) bool {
	_, err := BinaryPath(engine, version, bin)
	return err == nil
}

// Size returns the on-disk size in bytes of an installed distribution
// (0 when not installed).
func Size(engine, version string) int64 {
	base, err := imageRoot(engine, version)
	if err != nil {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(base, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// Install downloads (via mirror when non-empty) and extracts engine@version.
// Series versions like "8.0" resolve to the latest known patch release.
func Install(ref PackageRef, mirror string, stdout io.Writer) error {
	if Installed(ref.Engine, ref.Version) {
		fmt.Fprintf(stdout, "%s already installed\n", ref)
		return nil
	}

	ix, info, err := metadata.EnsurePackages(ref.Engine, ref.Version, mirror)
	if err != nil {
		return err
	}
	ref.Version = info.Version // series like "8.0" resolved to full version
	if Installed(ref.Engine, ref.Version) {
		fmt.Fprintf(stdout, "%s already installed\n", ref)
		return nil
	}

	pkg, err := info.Select(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	url := ix.DownloadURL(pkg)
	fmt.Fprintf(stdout, "resolved %s -> %s (%s)\n", ref, ref.Version, pkg.Filename)
	fmt.Fprintf(stdout, "downloading %s\n", url)

	archive, err := download(url, pkg.MD5, pkg.Size, stdout)
	if err != nil && pkg.FallbackURL != "" && pkg.FallbackURL != url {
		// old releases vanish from the GA CDN but stay on the archives
		// endpoints — retry with the recorded fallback
		fmt.Fprintf(stdout, "primary failed (%v); trying fallback %s\n", err, pkg.FallbackURL)
		archive, err = download(pkg.FallbackURL, pkg.MD5, 0, stdout) // fallback size unknown
	}
	if err != nil {
		return err
	}
	defer os.Remove(archive)

	base, err := imageRoot(ref.Engine, ref.Version)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(base); err != nil {
		return err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "extracting to %s\n", base)
	rel, err := extract(pkg.Kind, archive, base)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ".dbpod-root"), []byte(rel+"\n"), 0o644); err != nil {
		return err
	}
	postInstall(base, stdout)
	fmt.Fprintf(stdout, "installed %s\n", ref)
	return nil
}

// postInstall applies platform-specific fixes and hints.
func postInstall(base string, stdout io.Writer) {
	if runtime.GOOS == "darwin" {
		_ = clearQuarantine(base) // best effort; HTTP downloads carry no quarantine
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(stdout, "note: MySQL on Windows requires the Microsoft Visual C++ Redistributable;")
		fmt.Fprintln(stdout, "      if mysqld fails to start, install it from https://aka.ms/vs/17/release/vc_redist.x64.exe")
	}
}

// clearQuarantine removes the macOS com.apple.quarantine xattr recursively.
func clearQuarantine(base string) error {
	return exec.Command("xattr", "-rd", "com.apple.quarantine", base).Run()
}

// download fetches url to a temp file, verifying MD5 when known. Page-listed
// sizes are display-rounded and only used for progress display.
func download(url, md5sum string, size int64, stdout io.Writer) (string, error) {
	tmp, err := os.CreateTemp("", "dbpod-download-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}
	if size == 0 {
		size = resp.ContentLength
	}

	h := md5.New()
	var got int64
	lastMB := int64(-1)
	buf := make([]byte, 1<<20)
	for {
		n, err := resp.Body.Read(buf)
		got += int64(n)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return "", werr
			}
			h.Write(buf[:n])
			if mb := got >> 20; mb != lastMB { // print on MB change only
				lastMB = mb
				if size > 0 {
					fmt.Fprintf(stdout, "\r  %d / %d MB (%d%%)", mb, size>>20, got*100/size)
				} else {
					fmt.Fprintf(stdout, "\r  %d MB", mb)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	fmt.Fprintln(stdout)

	// note: page-listed sizes are display-rounded, so only the MD5 from the
	// same page is an authoritative integrity check.
	if md5sum != "" {
		sum := hex.EncodeToString(h.Sum(nil))
		if sum != strings.ToLower(md5sum) {
			return "", fmt.Errorf("md5 mismatch: got %s, want %s", sum, md5sum)
		}
		fmt.Fprintf(stdout, "md5 verified (%s)\n", sum)
	} else {
		fmt.Fprintf(stdout, "warning: no md5 published for this package, integrity not verified\n")
	}
	return tmp.Name(), nil
}
