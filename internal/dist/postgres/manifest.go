// Package postgres implements the dist.Catalog for PostgreSQL:
//
//   - linux: PGDG repositories (apt Packages.gz / yum primary.xml.gz) are
//     traversed to build the version list — versions absent from PGDG are
//     not supported; the server/client/dependency .debs/.rpms are extracted
//     into a portable engine directory (bin/lib/share/shared_libs)
//   - win/macOS: portable EDB binaries zips, probed per PGDG version
package postgres

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

//go:embed postgres.json
var manifestJSON []byte

// pgManifest maps PG versions to per-platform EDB artifacts.
type pgManifest struct {
	Versions map[string]map[string]pgArtifact `json:"versions"` // "17.2" → "darwin-arm64" → artifact
}

// pgArtifact is one downloadable EDB zip.
type pgArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"` // empty: integrity not verified (warn)
	Size   int64  `json:"size,omitempty"`
	Kind   string `json:"kind"` // zip
}

// platformKey maps GOOS/GOARCH to the EDB artifact key. Empty for linux:
// linux binaries come from PGDG, not EDB.
func platformKey(goos, goarch string) string {
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "darwin-arm64"
	case goos == "darwin" && goarch == "amd64":
		return "darwin-amd64"
	case goos == "windows" && goarch == "amd64":
		return "windows-amd64"
	default:
		return ""
	}
}

// loadManifest parses the embedded manifest.
func loadManifest() (*pgManifest, error) {
	var m pgManifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("postgres manifest: %w", err)
	}
	return &m, nil
}

// compareVersions compares dotted numeric versions: >0 when a is newer.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		an, bn := numPart(as, i), numPart(bs, i)
		if an != bn {
			return an - bn
		}
	}
	return 0
}

func numPart(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimRight(parts[i], "abcdefghijklmnopqrstuvwxyz"))
	if err != nil {
		return 0
	}
	return n
}

// latestInMajor returns the newest manifest version with the given major.
func (m *pgManifest) latestInMajor(major string) (string, error) {
	prefix := major + "."
	best := ""
	for v := range m.Versions {
		if strings.HasPrefix(v, prefix) && compareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no postgres %s.x version in manifest", major)
	}
	return best, nil
}

// edbOSName maps GOOS to the EDB download naming.
func edbOSName(goos string) string {
	if goos == "darwin" {
		return "osx"
	}
	return goos
}

// edbArchName maps GOARCH to the EDB download naming.
func edbArchName(goarch string) string {
	if goarch == "amd64" {
		return "x64"
	}
	return goarch
}

// probeEDB checks whether the EDB portable zip of a PG version exists for
// the platform (HEAD probe of the constructed URL).
func probeEDB(version, goos, goarch string) (string, bool) {
	var url string
	if goos == "darwin" {
		url = fmt.Sprintf("https://get.enterprisedb.com/postgresql/postgresql-%s-1-osx-binaries.zip", version)
	} else {
		url = fmt.Sprintf("https://get.enterprisedb.com/postgresql/postgresql-%s-1-%s-%s-binaries.zip",
			version, edbOSName(goos), edbArchName(goarch))
	}
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return url, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")
	resp, err := httpClient.Do(req)
	if err != nil {
		return url, false
	}
	resp.Body.Close()
	return url, resp.StatusCode == 200
}

// manifestForVersion returns the EDB artifact of a PG version for the
// platform key, or nil when the manifest has no such version.
func (m *pgManifest) artifactFor(version, platform string) *pgArtifact {
	platforms, ok := m.Versions[version]
	if !ok {
		return nil
	}
	if a, ok := platforms[platform]; ok {
		return &a
	}
	return nil
}
