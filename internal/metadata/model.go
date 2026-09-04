// Package metadata discovers MySQL engine versions and installable packages
// from the official web pages, with a 24h incremental local cache.
//
// Data sources (see docs/download-link-acquisition.md):
//   - GA page:    https://dev.mysql.com/downloads/mysql/          (latest series)
//   - Archive:    https://downloads.mysql.com/archives/community/ (all versions)
package metadata

import (
	"strings"
	"time"
)

// Package describes one downloadable archive of an engine version.
type Package struct {
	Filename    string `json:"filename"`
	URL         string `json:"url"`                    // primary download URL (official CDN rule)
	FallbackURL string `json:"fallback_url,omitempty"` // file endpoint if CDN rule misses
	Size        int64  `json:"size_bytes,omitempty"`
	MD5         string `json:"md5,omitempty"`
	SHA256      string `json:"sha256,omitempty"`     // engines with sha256-published checksums (e.g. EDB)
	OS          string `json:"os"`                   // darwin | linux | windows
	Arch        string `json:"arch"`                 // amd64 | arm64
	OSVersion   string `json:"os_version,omitempty"` // e.g. "macos15", "glibc2.28"
	Kind        string `json:"kind"`                 // tar.gz | tar.xz | zip | tar | dmg | msi
	Variant     string `json:"variant,omitempty"`    // "" | minimal | test | debug-test | debug
	Source      string `json:"source"`               // ga | archive
	Description string `json:"description,omitempty"`

	// PGDG linux pipeline (non-mysql engines): companion archives extracted
	// alongside the main one, and prefix-mapping rules applied during
	// extraction (srcPrefix → dstPrefix).
	DepURLs      []DepArchive `json:"dep_urls,omitempty"`
	ExtractRules [][2]string  `json:"extract_rules,omitempty"`
	RootDir      string       `json:"root_dir,omitempty"` // top-level dir containing bin/ (empty: auto-detect)
}

// DepArchive is a companion archive (e.g. a runtime-dependency .deb)
// downloaded and extracted together with the main package.
type DepArchive struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Kind   string `json:"kind"` // deb | rpm
}

// VersionInfo holds everything known about one engine version.
type VersionInfo struct {
	Version         string    `json:"version"` // full version, e.g. 8.0.46
	Series          string    `json:"series"`  // major.minor, e.g. 8.0
	LTS             bool      `json:"lts,omitempty"`
	Latest          bool      `json:"latest,omitempty"` // listed on the GA page
	PackagesFetched bool      `json:"packages_fetched,omitempty"`
	Packages        []Package `json:"packages,omitempty"`
}

// Index is the persisted metadata cache for one engine.
type Index struct {
	Engine    string                  `json:"engine"`
	FetchedAt time.Time               `json:"fetched_at"`
	BaseURL   string                  `json:"base_url,omitempty"` // parent directory of the metadata file; relative package URLs resolve against it
	Versions  map[string]*VersionInfo `json:"versions"`           // key: full version
}

// RefreshInterval is how long a cached index stays fresh.
const RefreshInterval = 24 * time.Hour

// Fresh reports whether the index is younger than RefreshInterval.
func (ix *Index) Fresh() bool {
	return !ix.FetchedAt.IsZero() && time.Since(ix.FetchedAt) < RefreshInterval
}

// DownloadURL resolves the download URL of a package. Absolute URLs are used
// as-is; relative URLs are joined with the metadata's parent directory
// (BaseURL) — that is how mirrors serve both metadata and engine files from
// the same base.
func (ix *Index) DownloadURL(p *Package) string {
	if strings.HasPrefix(p.URL, "http://") || strings.HasPrefix(p.URL, "https://") {
		return p.URL
	}
	return strings.TrimSuffix(ix.BaseURL, "/") + "/" + p.URL
}

// Version returns the version info for a full version string, or nil.
func (ix *Index) Version(v string) *VersionInfo {
	return ix.Versions[v]
}

// ListVersions returns all known versions sorted newest first.
func (ix *Index) ListVersions() []string {
	out := make([]string, 0, len(ix.Versions))
	for v := range ix.Versions {
		out = append(out, v)
	}
	sortVersions(out)
	return out
}
