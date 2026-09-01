package metadata

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	gaIndexURL     = "https://dev.mysql.com/downloads/mysql/"
	archiveBaseURL = "https://downloads.mysql.com/archives/community/"
)

// cdnPrefix is the replaceable prefix of official download URLs (see docs §5).
const cdnPrefix = "https://cdn.mysql.com/Downloads"

// GA page os ids.
const (
	osIDMacOS   = "33"
	osIDLinux   = "2"
	osIDWindows = "3"
)

// CDNURL builds the absolute official download URL for a package file.
func CDNURL(series, filename string) string {
	return OfficialDownloadsBase + "/MySQL-" + series + "/" + filename
}

// RelURL builds the relative download URL of a package file, as stored in
// the generated metadata. It resolves against the parent directory of the
// metadata file (official downloads base or a mirror base).
func RelURL(series, filename string) string {
	return fmt.Sprintf("MySQL-%s/%s", series, filename)
}

// FetchOption tunes HTTP behaviour.
type FetchOption struct {
	Timeout time.Duration
}

func httpClient(opt FetchOption) *http.Client {
	t := opt.Timeout
	if t <= 0 {
		t = 60 * time.Second
	}
	return &http.Client{Timeout: t}
}

func get(url string, opt FetchOption) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// A current-Chromium client hint is required; the download pages reject
	// requests without it. Note: do NOT spoof a browser User-Agent here —
	// the edge rejects UA/browser-fingerprint mismatches, while the default
	// client UA passes fine.
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="150"`)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := httpClient(opt).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// FetchGAIndexVersions lists the latest version series from the GA page.
func FetchGAIndexVersions(opt FetchOption) ([]VersionInfo, error) {
	html, err := get(gaIndexURL, opt)
	if err != nil {
		return nil, err
	}
	versions := parseGAIndexVersions(html)
	if len(versions) == 0 {
		return nil, fmt.Errorf("GA page: no versions parsed from %s", gaIndexURL)
	}
	return versions, nil
}

// FetchArchiveVersions lists every historical version from the archive page.
func FetchArchiveVersions(opt FetchOption) ([]string, error) {
	html, err := get(archiveBaseURL, opt)
	if err != nil {
		return nil, err
	}
	versions := parseArchiveVersions(html)
	if len(versions) == 0 {
		return nil, fmt.Errorf("archive page: no versions parsed from %s", archiveBaseURL)
	}
	return versions, nil
}

// FetchGAPackages lists packages of the latest patch release of a series
// (e.g. "8.0") by querying the GA page for each relevant OS.
func FetchGAPackages(series string, opt FetchOption) ([]Package, error) {
	var all []Package
	for _, osID := range []string{osIDMacOS, osIDLinux, osIDWindows} {
		url := fmt.Sprintf("%s?version=%s&os=%s", gaIndexURL, series, osID)
		html, err := get(url, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, parsePackageRows(html, "ga")...)
	}
	return all, nil
}

// FetchArchivePackages lists packages of a historical full version
// (e.g. "9.7.1") from the archive page. The os parameter selects which
// platform section is rendered server-side, so all relevant OS ids are
// queried and merged.
func FetchArchivePackages(version string, opt FetchOption) ([]Package, error) {
	var all []Package
	for _, osID := range []string{osIDMacOS, osIDLinux, osIDWindows} {
		url := fmt.Sprintf("%s?tpl=version&os=%s&version=%s&osva=", archiveBaseURL, osID, version)
		html, err := get(url, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, parsePackageRows(html, "archive")...)
	}
	return all, nil
}
