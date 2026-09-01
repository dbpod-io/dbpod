package metadata

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The generated metadata lives in the dbpod repository and is embedded into
// the binary at build time. At runtime dbpod tries, in order: a configured
// mirror (base URL = parent directory of the metadata file), the repository
// via CDN, and finally the embedded copy. Package URLs in the metadata may
// be relative and resolve against the parent directory of wherever the
// metadata was fetched from (official: cdn.mysql.com/Downloads, mirror: the
// mirror base).
const (
	repoOwner  = "shapled"
	repoName   = "dbpod"
	repoBranch = "main"
	repoPath   = "internal/metadata/data/mysql.json"

	// OfficialDownloadsBase is the BaseURL for official (repository or
	// embedded) metadata: relative package URLs resolve against it.
	OfficialDownloadsBase = "https://cdn.mysql.com/Downloads"
)

// repoURLs lists fetch sources in priority order (CDN first, GitHub raw as
// backup).
var repoURLs = []string{
	fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s@%s/%s", repoOwner, repoName, repoBranch, repoPath),
	fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", repoOwner, repoName, repoBranch, repoPath),
}

// fetchIndexURL downloads and decodes one metadata URL.
func fetchIndexURL(url string) (*Index, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var ix Index
	if err := json.NewDecoder(resp.Body).Decode(&ix); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	if len(ix.Versions) == 0 {
		return nil, fmt.Errorf("%s: empty index", url)
	}
	return &ix, nil
}

// FetchFromMirror fetches metadata from a mirror base (the parent directory
// that also serves the engine files).
func FetchFromMirror(mirror string) (*Index, error) {
	ix, err := fetchIndexURL(strings.TrimSuffix(mirror, "/") + "/mysql.json")
	if err != nil {
		return nil, err
	}
	ix.BaseURL = strings.TrimSuffix(mirror, "/")
	return ix, nil
}

// FetchFromRepo downloads the latest generated metadata from the repository.
// The first source that yields a parseable index wins.
func FetchFromRepo() (*Index, error) {
	var lastErr error
	for _, url := range repoURLs {
		ix, err := fetchIndexURL(url)
		if err != nil {
			lastErr = err
			continue
		}
		ix.BaseURL = OfficialDownloadsBase
		return ix, nil
	}
	return nil, fmt.Errorf("all metadata sources failed: %w", lastErr)
}
