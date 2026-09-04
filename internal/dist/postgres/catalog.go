package postgres

import (
	"fmt"
	"strings"
	"sync"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/metadata"
)

// Catalog implements dist.Catalog for PostgreSQL: the version list comes
// from PGDG repository traversal (the authoritative history — versions
// absent from PGDG are not supported), and win/macOS packages are probed
// against the EDB per-version zips.
type Catalog struct{}

func init() { dist.RegisterCatalog(Catalog{}) }

func (Catalog) Engine() string { return "postgres" }

// SeriesOf: PostgreSQL majors ARE the series — "17.11" belongs to
// series "17" (minor releases are patches).
func (Catalog) SeriesOf(version string, lts, isLatest bool) string { return majorOf(version) }

func (Catalog) EnsureVersions(mirror string) (*metadata.Index, error) {
	// PGDG traversal is the authoritative version list...
	ix, err := traversePGDG()
	if err != nil {
		return nil, err
	}
	// ...and every version is probed for EDB win/macOS portable zips
	// (bounded-concurrency HEADs; the result persists in the cached index).
	if err := probeEDBAvailability(ix); err != nil {
		return ix, err
	}
	return ix, nil
}

// probeEDBAvailability HEAD-probes the EDB osx/windows zips of every
// PG version and appends the package entries to the index.
func probeEDBAvailability(ix *metadata.Index) error {
	var mu sync.Mutex
	versions := make([]string, 0, len(ix.Versions))
	for v := range ix.Versions {
		versions = append(versions, v)
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, v := range versions {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, plat := range []struct{ os, arch, key string }{
				{"darwin", "arm64", "darwin-arm64"},
				{"darwin", "amd64", "darwin-amd64"},
				{"windows", "amd64", "windows-amd64"},
			} {
				if url, ok := probeEDB(v, plat.os, plat.arch); ok {
					mu.Lock()
					vi := ix.Versions[v]
					vi.Packages = append(vi.Packages, metadata.Package{
						Filename: fmt.Sprintf("postgresql-%s-%s-binaries.zip", v, plat.key),
						URL:      url,
						Kind:     "zip",
						OS:       plat.os,
						Arch:     plat.arch,
						RootDir:  "pgsql",
					})
					mu.Unlock()
				}
			}
		}(v)
	}
	wg.Wait()
	return nil
}

func (Catalog) ResolveVersion(version, mirror string) (string, error) {
	if strings.Count(version, ".") >= 2 {
		return version, nil // already full
	}
	ix, err := traversePGDG()
	if err != nil {
		return "", err
	}
	best := ""
	for _, v := range ix.ListVersions() {
		// exact match counts too: a PG "full" version is two segments
		if (v == version || strings.HasPrefix(v, version+".")) && compareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no known version in series %s for postgres", version)
	}
	return best, nil
}

func (Catalog) Resolve(version, goos, goarch, mirror string) (*metadata.Package, error) {
	switch goos {
	case "linux":
		return resolvePGDG(version)
	case "darwin", "windows":
		return resolveEDB(version, goos, goarch)
	default:
		return nil, fmt.Errorf("unsupported platform %s/%s for postgres", goos, goarch)
	}
}

// resolveEDB returns the EDB zip package of a PG version for darwin/windows:
// the manifest may pin an artifact; otherwise the conventional URL is
// probed (a 404 means EDB does not ship that version/platform).
func resolveEDB(version, goos, goarch string) (*metadata.Package, error) {
	m, err := loadManifest()
	if err != nil {
		return nil, err
	}
	if a := m.artifactFor(version, platformKey(goos, goarch)); a != nil {
		return &metadata.Package{
			Filename: fmt.Sprintf("postgresql-%s-%s-binaries.zip", version, platformKey(goos, goarch)),
			URL:      a.URL,
			SHA256:   a.SHA256,
			Size:     a.Size,
			Kind:     a.Kind,
			OS:       goos,
			Arch:     goarch,
			RootDir:  "pgsql",
		}, nil
	}
	url, ok := probeEDB(version, goos, goarch)
	if !ok {
		return nil, fmt.Errorf("no EDB package of postgres %s for %s/%s", version, goos, goarch)
	}
	return &metadata.Package{
		Filename: fmt.Sprintf("postgresql-%s-%s-binaries.zip", version, platformKey(goos, goarch)),
		URL:      url,
		Kind:     "zip",
		OS:       goos,
		Arch:     goarch,
		RootDir:  "pgsql",
	}, nil
}
