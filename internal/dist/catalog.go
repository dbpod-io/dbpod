package dist

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/shapled/dbpod/internal/metadata"
)

// Catalog abstracts where an engine's versions come from and how a version
// resolves to an installable package for the current platform.
//
// Two shapes exist today:
//   - mysql: metadata crawled from the official pages with a 24h cache
//   - postgres: PGDG repository traversal (linux) + EDB binaries (win/mac)
//
// New engines register a Catalog via RegisterCatalog.
type Catalog interface {
	Engine() string

	// SeriesOf returns the release series a version belongs to. Engines
	// differ: mysql = major.minor ("8.0"; the newest calendar release is
	// "innovation", older calendar releases keep their major.minor series);
	// postgres = major ("17"). isLatest marks the globally newest version.
	SeriesOf(version string, lts, isLatest bool) string

	// EnsureVersions returns the version index, refreshing it when stale.
	// mirror affects which cached source is considered current.
	EnsureVersions(mirror string) (*metadata.Index, error)

	// ResolveVersion maps a possibly-series version ("8.0") to a full
	// version ("8.0.46"), preferring locally installed matches. Full
	// versions pass through unchanged.
	ResolveVersion(version, mirror string) (string, error)

	// Resolve returns the installable package of version for the platform,
	// with its download URL already resolved (mirror applied when set).
	Resolve(version, goos, goarch, mirror string) (*metadata.Package, error)
}

var catalogs = map[string]Catalog{}

// RegisterCatalog adds an engine catalog (called by catalog impls in init).
func RegisterCatalog(c Catalog) {
	catalogs[c.Engine()] = c
}

// CatalogFor returns the catalog of the named engine.
func CatalogFor(engine string) (Catalog, error) {
	c, ok := catalogs[engine]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	return c, nil
}

// Catalogs returns all registered catalogs sorted by engine name.
func Catalogs() []Catalog {
	names := make([]string, 0, len(catalogs))
	for name := range catalogs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Catalog, 0, len(names))
	for _, n := range names {
		out = append(out, catalogs[n])
	}
	return out
}

// ResolveVersion resolves a version for the named engine (series forms
// allowed). Exported helper for the cmd layer.
func ResolveVersion(engine, version, mirror string) (string, error) {
	cat, err := CatalogFor(engine)
	if err != nil {
		// engine without a catalog (e.g. not yet wired): pass through
		return version, nil
	}
	return cat.ResolveVersion(version, mirror)
}

// EnsureInstalled installs engine@version when missing.
func EnsureInstalled(engineName, version, mirror string, stdout io.Writer) error {
	if Installed(engineName, version) {
		return nil
	}
	fmt.Fprintf(stdout, "engine %s@%s not installed yet\n", engineName, version)
	return Install(PackageRef{Engine: engineName, Version: version}, mirror, stdout)
}

// MysqlCatalog implements Catalog on top of the metadata crawl pipeline.
type MysqlCatalog struct{}

func (MysqlCatalog) Engine() string { return "mysql" }

func (MysqlCatalog) EnsureVersions(mirror string) (*metadata.Index, error) {
	return metadata.EnsureVersions("mysql", mirror)
}

func (MysqlCatalog) ResolveVersion(version, mirror string) (string, error) {
	if strings.Count(version, ".") >= 2 {
		return version, nil // already full
	}
	// prefer a locally installed match within the series
	local, err := ListLocal()
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
	// latest known in that series
	ix, err := metadata.EnsureVersions("mysql", mirror)
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

// SeriesOf: mysql groups by major.minor. Calendar releases (26.x) are
// "innovation" unless they are an LTS release superseded by a newer one
// (then they keep their major.minor series, e.g. "26.10").
func (MysqlCatalog) SeriesOf(version string, lts, isLatest bool) string {
	if calendarVersion(version) {
		if !lts || isLatest {
			return "innovation"
		}
	}
	return majorMinor(version)
}

// calendarVersion reports whether the version uses calendar versioning
// (major >= 10, e.g. 26.7.0) instead of the classic scheme (5.7 ... 9.7).
// Calendar releases are innovation (non-LTS) or LTS: LTS calendar releases
// keep their own series, which callers mark via the LTS flag.
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

func (MysqlCatalog) Resolve(version, goos, goarch, mirror string) (*metadata.Package, error) {
	ix, info, err := metadata.EnsurePackages("mysql", version, mirror)
	if err != nil {
		return nil, err
	}
	pkg, err := info.Select(goos, goarch)
	if err != nil {
		return nil, err
	}
	out := *pkg
	out.URL = ix.DownloadURL(&out)
	if mirror != "" {
		// mirror rewrites the official GA CDN prefix
		out.URL = strings.TrimSuffix(mirror, "/") + strings.TrimPrefix(out.URL, metadata.OfficialDownloadsBase)
	}
	return &out, nil
}

func init() {
	RegisterCatalog(MysqlCatalog{})
}
