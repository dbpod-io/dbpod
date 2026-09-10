package mysql

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/metadata"
)

// Provider is the builtin provider for MySQL: the mysql-family lifecycle
// machinery (Engine) plus version resolution and download plans from the
// generated metadata (embedded copy seeded into the configuration
// directory, refreshed via `registry update`).
type Provider struct {
	*Engine
}

func init() { engine.Register(&Provider{Engine: New(DefaultProfile())}) }

// SeriesOf: mysql groups by major.minor ("8.0"); calendar releases (26.x)
// collapse into "innovation" — the newest calendar version represents the
// series.
// calendarVersion reports whether the version uses calendar versioning
// (major >= 10, e.g. 26.7.0) instead of the classic scheme (5.7 ... 9.7).
func calendarVersion(version string) bool {
	major, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 10
}

func majorMinor(version string) string {
	major, rest, _ := strings.Cut(version, ".")
	if rest == "" {
		return major
	}
	minor, _, _ := strings.Cut(rest, ".")
	return major + "." + minor
}

func (p *Provider) SeriesOf(version string, lts, isLatest bool) []string {
	if calendarVersion(version) {
		if !lts || isLatest {
			return []string{"innovation"}
		}
		return []string{majorMinor(version)}
	}
	return []string{majorMinor(version)}
}

func (p *Provider) EnsureVersions() (*metadata.Index, error) {
	return metadata.EnsureBuiltin("mysql")
}

func (p *Provider) ResolveVersion(version string) (string, error) {
	return p.resolveVersion(version)
}

// resolveVersion maps a possibly-series version ("8.0") to a full version,
// preferring a locally installed match, then the latest known release.
func (p *Provider) resolveVersion(version string) (string, error) {
	if strings.Count(version, ".") >= 2 {
		return version, nil // already full
	}
	local, err := dist.ListLocal()
	if err != nil {
		return "", err
	}
	best := ""
	for _, l := range local {
		if l.Engine == "mysql" && strings.HasPrefix(l.Version, version+".") && l.Version > best {
			best = l.Version
		}
	}
	if best != "" {
		return best, nil
	}
	ix, err := metadata.EnsureBuiltin("mysql")
	if err != nil {
		return "", fmt.Errorf("cannot resolve series %q: %w (install a full version, e.g. mysql@8.0.35)", version, err)
	}
	for _, v := range ix.ListVersions() {
		if strings.HasPrefix(v, version+".") {
			return v, nil
		}
	}
	return "", fmt.Errorf("no known version in series %s for mysql", version)
}

// ResolveDownload returns the package of version for the platform.
func (p *Provider) ResolveDownload(version, goos, goarch string) (engine.DownloadPlan, error) {
	ix, info, err := metadata.EnsurePackages("mysql", version)
	if err != nil {
		return engine.DownloadPlan{}, err
	}
	pkg, err := info.Select(goos, goarch)
	if err != nil {
		return engine.DownloadPlan{}, err
	}
	out := *pkg
	out.URL = ix.DownloadURL(&out)
	return engine.DownloadPlan{Version: info.Version, Main: engine.DownloadFile{
		URL: out.URL, MD5: out.MD5, Size: out.Size, Kind: out.Kind,
	}}, nil
}

// Install uses the shared dist pipeline (download, verify, extract).
func (p *Provider) Install(plan engine.DownloadPlan, base string, stdout io.Writer) error {
	return dist.InstallBase(plan, base, stdout)
}
