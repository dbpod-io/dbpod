package postgres

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/metadata"
)

// Provider implements dist.Provider for PostgreSQL: the version list comes
// from PGDG repository traversal (the authoritative history — versions
// absent from PGDG are not supported), and win/macOS packages are probed
// against the EDB per-version zips.
type Provider struct{}

func init() { dist.RegisterProvider(Provider{}) }

func (Provider) Engine() string { return "postgres" }

// SeriesOf: PostgreSQL majors ARE the series — "17.11" belongs to
// series "17" (minor releases are patches).
func (Provider) SeriesOf(version string, lts, isLatest bool) []string {
	return []string{majorOf(version)}
}

// EnsureVersions returns the version index with a 24h cache: the PGDG
// traversal issues dozens of requests, so repeated commands reuse the
// cached index. Each version is also probed for EDB win/macOS zips so
// they are installable on those platforms.
func (Provider) EnsureVersions() (*metadata.Index, error) {
	ix, err := metadata.Load("postgres")
	if err == nil && ix != nil && ix.Fresh() {
		return ix, nil
	}
	ix, err = traversePGDG()
	if err != nil {
		return nil, err
	}
	ix.FetchedAt = time.Now()
	probeEDBEntries(ix)
	if err := metadata.Save("postgres", ix); err != nil {
		return ix, nil // usable even if caching failed
	}
	return ix, nil
}

// probeEDBEntries HEAD-probes the EDB osx/windows zips of every PG version
// and appends darwin/windows package entries to the index.
func probeEDBEntries(ix *metadata.Index) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, vi := range ix.Versions {
		wg.Add(1)
		go func(vi *metadata.VersionInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, plat := range []struct{ os, arch, key string }{
				{"darwin", "arm64", "darwin-arm64"},
				{"darwin", "amd64", "darwin-amd64"},
				{"windows", "amd64", "windows-amd64"},
			} {
				if url, ok := probeEDB(vi.Version, plat.os, plat.arch); ok {
					vi.Packages = append(vi.Packages, metadata.Package{
						Filename: fmt.Sprintf("postgresql-%s-%s-binaries.zip", vi.Version, plat.key),
						URL:      url,
						Kind:     "zip",
						OS:       plat.os,
						Arch:     plat.arch,
					})
				}
			}
		}(vi)
	}
	wg.Wait()
}

// ResolveVersion maps a possibly-series version ("17") to a full version
// ("17.11") using the PGDG index. PG versions are major.minor, so any
// dotted form is already full (series = bare major).
func (Provider) ResolveVersion(version, mirror string) (string, error) {
	if strings.Contains(version, ".") {
		return version, nil // already full
	}
	ix, err := traversePGDG()
	if err != nil {
		return "", err
	}
	best := ""
	for _, v := range ix.ListVersions() {
		if strings.HasPrefix(v, version+".") && compareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no known version in series %s for postgres", version)
	}
	return best, nil
}

func (Provider) ResolveDownload(version, goos, goarch string) (dist.DownloadPlan, error) {
	switch goos {
	case "linux":
		return resolvePGDG(version)
	case "darwin", "windows":
		return resolveEDB(version, goos, goarch)
	default:
		return dist.DownloadPlan{}, fmt.Errorf("unsupported platform %s/%s for postgres", goos, goarch)
	}
}

// resolveEDB returns the EDB zip package of a PG version for darwin/windows:
// the manifest may pin an artifact; otherwise the conventional URL is
// probed (a 404 means EDB does not ship that version/platform).
func resolveEDB(version, goos, goarch string) (dist.DownloadPlan, error) {
	m, err := loadManifest()
	if err != nil {
		return dist.DownloadPlan{}, err
	}
	platform := platformKey(goos, goarch)
	a := m.artifactFor(version, platform)

	var main dist.DownloadFile
	if a != nil {
		main = dist.DownloadFile{
			URL:     a.URL,
			SHA256:  a.SHA256,
			Size:    a.Size,
			Kind:    a.Kind,
			RootDir: "pgsql",
		}
	} else {
		url, ok := probeEDB(version, goos, goarch)
		if !ok {
			return dist.DownloadPlan{}, fmt.Errorf("no EDB package of postgres %s for %s/%s", version, goos, goarch)
		}
		main = dist.DownloadFile{URL: url, Kind: "zip", RootDir: "pgsql"}
	}
	return dist.DownloadPlan{Version: version, Main: main}, nil
}
