// Package external instantiates config-declared engines: engines whose
// entire description (lifecycle profile, download layout, version index
// location) lives in the global config instead of compiled-in code.
package external

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/fetch"
	"github.com/dbpod-io/dbpod/internal/globalconfig"
	"github.com/dbpod-io/dbpod/internal/metadata"
)

// Provider serves a config-declared engine: versions from the configured
// index (native metadata.Index JSON, 24h cache), download plans from the
// configured URL templates.
type Provider struct {
	manifest *globalconfig.EngineManifest
	base     string // file base of the default source ("" = absolute URLs only)
}

var _ dist.Provider = (*Provider)(nil)

func (p *Provider) Engine() string { return p.manifest.Name }

// EnsureVersions returns the index from index_url. Once fetched, the
// cache is used indefinitely — there is no auto-refresh; `dbpod registry
// update` refreshes it explicitly.
func (p *Provider) EnsureVersions() (*metadata.Index, error) {
	name := p.manifest.Name
	if ix, err := metadata.Load(name); err == nil && ix != nil {
		return ix, nil
	}
	if p.manifest.IndexURL == "" {
		return nil, fmt.Errorf("%s: no index_url configured; install an exact version instead (e.g. %s@11.4.5)", name, name)
	}
	ix, err := fetchIndex(p.manifest.IndexURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if ix.Engine != name {
		return nil, fmt.Errorf("%s: index at %s declares engine %q", name, p.manifest.IndexURL, ix.Engine)
	}
	ix.FetchedAt = time.Now()
	if err := metadata.Save(name, ix); err != nil {
		return ix, nil // usable even if caching failed
	}
	return ix, nil
}

// fetchIndex downloads and decodes a native metadata.Index document
// through the fetch layer (scheme routing, proxy, audit).
func fetchIndex(url string) (*metadata.Index, error) {
	tmp, err := os.CreateTemp("", "dbpod-index-*")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := fetch.Fetch(context.Background(), url, tmp.Name()); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	var ix metadata.Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("decode index %s: %w", url, err)
	}
	if len(ix.Versions) == 0 {
		return nil, fmt.Errorf("index %s is empty", url)
	}
	return &ix, nil
}

// SeriesOf reads the series from the cached index; without one it falls
// back to the model's major.minor rule.
func (p *Provider) SeriesOf(version string, lts, isLatest bool) []string {
	if ix, err := metadata.Load(p.manifest.Name); err == nil && ix != nil {
		if v := ix.Version(version); v != nil && v.Series != "" {
			return []string{v.Series}
		}
	}
	return []string{metadata.SeriesOf(version)}
}

// ResolveVersion maps a series ("11.4") to the newest known full version
// using the index; full versions pass through even without an index.
func (p *Provider) ResolveVersion(version, mirror string) (string, error) {
	if p.manifest.IndexURL != "" {
		ix, err := p.EnsureVersions()
		if err != nil {
			return "", err
		}
		if v := ix.Version(version); v != nil {
			return version, nil
		}
		for _, v := range ix.ListVersions() { // newest first
			vi := ix.Version(v)
			if vi != nil && (vi.Series == version || strings.HasPrefix(v, version+".")) {
				return v, nil
			}
		}
		return "", fmt.Errorf("no known version in series %s for %s", version, p.manifest.Name)
	}
	// without an index only exact versions (major.minor.patch) are
	// installable — family versions are three-part
	if strings.Count(version, ".") >= 2 {
		return version, nil
	}
	return "", fmt.Errorf("%s: resolving series %q needs index_url; install an exact version instead", p.manifest.Name, version)
}

// ResolveDownload instantiates the platform's URL template and attaches
// the published checksum.
func (p *Provider) ResolveDownload(version, goos, goarch string) (dist.DownloadPlan, error) {
	if p.manifest.Download == nil || len(p.manifest.Download.Targets) == 0 {
		return dist.DownloadPlan{}, fmt.Errorf("%s: no download targets configured", p.manifest.Name)
	}
	t, ok := p.manifest.Download.Targets[goos+"/"+goarch]
	if !ok {
		return dist.DownloadPlan{}, fmt.Errorf("%s: no package for %s/%s (configured: %v)", p.manifest.Name, goos, goarch, targetKeys(p.manifest.Download.Targets))
	}
	rel := strings.ReplaceAll(t.URL, "{version}", version)
	if strings.Contains(rel, "{series}") {
		series := ""
		if ix, err := p.EnsureVersions(); err == nil {
			if vi := ix.Version(version); vi != nil {
				series = vi.Series
			}
		}
		rel = strings.ReplaceAll(rel, "{series}", series)
	}
	url, err := p.resolveURL(rel)
	if err != nil {
		return dist.DownloadPlan{}, err
	}
	plan := dist.DownloadPlan{
		Version: version,
		Main: dist.DownloadFile{
			URL:  url,
			Kind: t.Kind,
		},
	}
	if name := p.manifest.Download.Checksums; name != "" {
		sum, err := p.checksumOf(dirOf(rel), name, baseOf(rel))
		if err != nil {
			return dist.DownloadPlan{}, err
		}
		plan.Main.SHA256 = sum
	}
	if size := probeSize(url); size > 0 {
		plan.Main.Size = size
	}
	return plan, nil
}

// resolveURL joins a relative target URL with the configured source base;
// absolute URLs pass through.
func (p *Provider) resolveURL(u string) (string, error) {
	if isURL(u) {
		return u, nil
	}
	if p.base == "" {
		return "", fmt.Errorf("%s: download url %q is relative but no source base is configured (set providers.%s.sources)", p.manifest.Name, u, p.manifest.Name)
	}
	return strings.TrimSuffix(p.base, "/") + "/" + u, nil
}

// checksumOf downloads the sums file of a package directory and returns
// the hash recorded for name.
func (p *Provider) checksumOf(dir, sumsFile, name string) (string, error) {
	sumsURL, err := p.resolveURL(dir + "/" + sumsFile)
	if err != nil {
		return "", err
	}
	resp, err := fetch.HTTPClient().Get(sumsURL)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", sumsURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("no checksums for %s/%s (%s: status %d)", p.manifest.Name, name, sumsURL, resp.StatusCode)
	}
	var want string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("%s/%s not listed in %s", p.manifest.Name, name, sumsFile)
	}
	return want, nil
}

// probeSize HEADs the archive for a display size (best effort).
func probeSize(url string) int64 {
	resp, err := fetch.HTTPClient().Head(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	return resp.ContentLength
}

func targetKeys(ts map[string]globalconfig.DownloadTarget) []string {
	out := make([]string, 0, len(ts))
	for k := range ts {
		out = append(out, k)
	}
	return out
}

func isURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func dirOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

func baseOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
