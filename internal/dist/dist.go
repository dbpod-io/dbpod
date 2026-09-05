// Package dist manages install-free engine distributions: download, verify,
// extract and cache engine binaries under DBPOD_HOME/versions.
package dist

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dbpod-io/dbpod/internal/fetch"
	"github.com/dbpod-io/dbpod/internal/project"
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

// Remove deletes the cached distribution of engine@version, then prunes
// the now-empty version and engine parent directories so no husks remain.
func Remove(engine, version string) error {
	if !Installed(engine, version) {
		return fmt.Errorf("engine %s is not installed", engine+"@"+version)
	}
	base, err := imageRoot(engine, version)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(base); err != nil {
		return err
	}
	versionDir := filepath.Dir(base)
	if v, relErr := filepath.Rel(project.VersionsDirMust(), versionDir); relErr == nil && !strings.HasPrefix(v, "..") {
		_ = os.Remove(versionDir)               // only succeeds when empty
		_ = os.Remove(filepath.Dir(versionDir)) // engine dir: only when empty
	}
	return nil
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

	prov, err := ProviderFor(ref.Engine)
	if err != nil {
		return err
	}
	version, err := prov.ResolveVersion(ref.Version, mirror)
	if err != nil {
		return err
	}
	ref.Version = version // series like "8.0" resolved to full version
	if Installed(ref.Engine, ref.Version) {
		fmt.Fprintf(stdout, "%s already installed\n", ref)
		return nil
	}

	plan, err := prov.ResolveDownload(ref.Version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	main := plan.Main
	url := main.URL
	mainFallback := main.FallbackURL
	fmt.Fprintf(stdout, "resolved %s -> %s (%s)\n", ref, ref.Version, filepath.Base(url))
	fmt.Fprintf(stdout, "downloading %s\n", url)

	archive, err := fetchDownload(url, main.MD5, main.Size, stdout)
	if err != nil && mainFallback != "" && mainFallback != url {
		// old releases vanish from the GA CDN but stay on the archives
		// endpoints — retry with the recorded fallback
		fmt.Fprintf(stdout, "primary failed (%v); trying fallback %s\n", err, mainFallback)
		archive, err = fetchDownload(mainFallback, main.MD5, 0, stdout) // fallback size unknown
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

	// PGDG-style packages: companion dependency archives (shared libs) are
	// downloaded and extracted alongside the main archive
	var depArchives []string
	for i, dep := range plan.Deps {
		fmt.Fprintf(stdout, "downloading dependency %d/%d: %s\n", i+1, len(plan.Deps), dep.URL)
		depPath, derr := fetchDownload(dep.URL, "", 0, io.Discard)
		if derr != nil {
			return derr
		}
		depArchives = append(depArchives, depPath)
		defer os.Remove(depPath)
		_ = dep.SHA256 // checksums for dependency archives: verify when published
	}

	fmt.Fprintf(stdout, "extracting to %s\n", base)

	// deb/rpm pipelines extract with prefix-mapping rules (bin/lib/share/
	// shared_libs); regular archives use the generic extractor
	if main.Kind == "deb" || main.Kind == "rpm" {
		if err := extractEnginePackage(main.Kind, archive, depArchives, base, main.ExtractRules); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed %s\n", ref)
		return nil
	}

	rel, err := extract(main.Kind, archive, base)
	if err != nil {
		return err
	}
	if main.RootDir != "" { // explicit root hint wins over /bin/ probing
		rel = main.RootDir
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

// fetchDownload retrieves url through the fetch layer (scheme routing,
// proxy, audit) into a temp file, showing progress and verifying MD5.
func fetchDownload(url, md5sum string, size int64, stdout io.Writer) (string, error) {
	tmp, err := os.CreateTemp("", "dbpod-download-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	if size == 0 {
		size = -1
	}
	fetch.SetProgressHook(func(n int64) {
		if size > 0 {
			fmt.Fprintf(stdout, "\r  %d / %d MB (%d%%)", n>>20, size>>20, n*100/size)
		} else {
			fmt.Fprintf(stdout, "\r  %d MB", n>>20)
		}
	})
	defer fetch.SetProgressHook(nil)

	if _, err := fetch.Fetch(context.Background(), url, tmp.Name()); err != nil {
		return "", err
	}
	fmt.Fprintln(stdout)

	// page-listed sizes are display-rounded; the MD5 from the same page is
	// the authoritative integrity check.
	if md5sum != "" {
		f, err := os.Open(tmp.Name())
		if err != nil {
			return "", err
		}
		defer f.Close()
		h := md5.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
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
