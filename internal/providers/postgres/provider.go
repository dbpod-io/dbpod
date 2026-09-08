// Package postgres implements the dist.Provider for PostgreSQL:
//
//   - version discovery comes from the generated index
//     (versions.json, committed by cmd/pg-metadata-gen) — version probing
//     is a generation step, never runtime
//   - linux download: PGDG repository resolution at install time (baseline
//     selection, server/client debs/rpms extracted into a portable engine
//     directory) — internal/pgdg
//   - win/macOS download: EDB portable zips recorded in the index
package postgres

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/metadata"
	"github.com/dbpod-io/dbpod/internal/pgdg"
)

//go:embed versions.json
var versionsJSON []byte

type Provider struct{}

func init() {
	metadata.RegisterEmbedded("postgres", versionsJSON, false)
	dist.RegisterProvider(Provider{})
}

func (Provider) Engine() string { return "postgres" }

// SeriesOf: PostgreSQL majors ARE the series — "17.11" belongs to
// series "17" (minor releases are patches).
func (Provider) SeriesOf(version string, lts, isLatest bool) []string {
	return []string{pgdg.MajorOf(version)}
}

// versionsIndex decodes the embedded, generated version index.
func versionsIndex() (*metadata.Index, error) {
	var ix metadata.Index
	if err := json.Unmarshal(versionsJSON, &ix); err != nil {
		return nil, fmt.Errorf("postgres versions index: %w", err)
	}
	return &ix, nil
}

// EnsureVersions returns the generated version index. It is copied into
// the configuration directory on first use and never fetched at runtime;
// `registry update postgres` refreshes it.
func (Provider) EnsureVersions() (*metadata.Index, error) {
	return metadata.EnsureBuiltin("postgres")
}

// ResolveVersion maps a possibly-series version ("17") to a full version
// ("17.11") using the index.
func (Provider) ResolveVersion(version, mirror string) (string, error) {
	if strings.Contains(version, ".") {
		return version, nil // already full (series = bare major)
	}
	ix, err := versionsIndex()
	if err != nil {
		return "", err
	}
	for _, v := range ix.ListVersions() { // newest first
		vi := ix.Version(v)
		if vi != nil && (vi.Series == version || strings.HasPrefix(v, version+".")) {
			return v, nil
		}
	}
	return "", fmt.Errorf("no known version in series %s for postgres", version)
}

func (Provider) ResolveDownload(version, goos, goarch string) (dist.DownloadPlan, error) {
	switch goos {
	case "linux":
		return pgdg.Resolve(version)
	case "darwin", "windows":
		return resolveEDB(version, goos, goarch)
	default:
		return dist.DownloadPlan{}, fmt.Errorf("unsupported platform %s/%s for postgres", goos, goarch)
	}
}

// resolveEDB returns the EDB portable zip of a PG version for win/macOS,
// as recorded in the generated index.
func resolveEDB(version, goos, goarch string) (dist.DownloadPlan, error) {
	ix, err := versionsIndex()
	if err != nil {
		return dist.DownloadPlan{}, err
	}
	vi := ix.Version(version)
	if vi == nil {
		return dist.DownloadPlan{}, fmt.Errorf("postgres %s not in the version index (run: dbpod registry update postgres)", version)
	}
	for _, p := range vi.Packages {
		if p.OS == goos && p.Arch == goarch {
			return dist.DownloadPlan{
				Version: version,
				Main: dist.DownloadFile{
					URL:     p.URL,
					SHA256:  p.SHA256,
					Size:    p.Size,
					Kind:    p.Kind,
					RootDir: "pgsql",
				},
			}, nil
		}
	}
	return dist.DownloadPlan{}, fmt.Errorf("no EDB package of postgres %s for %s/%s", version, goos, goarch)
}
