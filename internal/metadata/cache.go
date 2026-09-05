package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dbpod-io/dbpod/internal/project"
)

// cachePath returns the on-disk cache file for an engine.
func cachePath(engine string) (string, error) {
	dir, err := project.MetadataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, engine+".json"), nil
}

// Load reads the cached index for engine, or returns nil when absent.
func Load(engine string) (*Index, error) {
	path, err := cachePath(engine)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("corrupt metadata cache %s: %w", path, err)
	}
	return &ix, nil
}

// Save persists the index for engine (runtime cache).
func Save(engine string, ix *Index) error {
	path, err := cachePath(engine)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Ensure returns a usable index for engine. Crawling is never involved:
//
//  1. a runtime cache younger than 24h is used as-is (unless it came from a
//     different mirror than requested now)
//  2. otherwise, when a mirror is configured, the mirror's metadata is tried
//     first; a mirror without metadata is unusable and reported as an error
//     (but does not abort — the chain continues)
//  3. otherwise/next, the generated metadata is fetched from the dbpod
//     repository via CDN and cached
//  4. when all network sources fail, the metadata embedded in the binary is
//     used (official download base)
func Ensure(engine, mirror string) (*Index, error) {
	ix, err := Load(engine)
	if err != nil {
		return nil, err
	}
	if ix != nil && ix.Fresh() && (mirror == "" || ix.BaseURL == mirror) {
		return ix, nil
	}

	if mirror != "" {
		mx, merr := FetchFromMirror(mirror)
		if merr != nil {
			fmt.Fprintf(os.Stderr, "error: mirror %s is unusable (no metadata): %v\n", mirror, merr)
		} else {
			_ = Save(engine, mx)
			return mx, nil
		}
	}

	repo, err := FetchFromRepo()
	if err != nil {
		embedded, embErr := Embedded(engine)
		if embErr != nil {
			return nil, fmt.Errorf("no metadata available: repo fetch failed (%v) and embedded fallback failed (%v)", err, embErr)
		}
		embedded.BaseURL = OfficialDownloadsBase
		return embedded, nil
	}
	if err := Save(engine, repo); err != nil {
		return repo, nil // usable even if caching failed
	}
	return repo, nil
}

// EnsurePackages returns the index plus the version info with its package
// list. All packages come from the generated metadata (mirror, repository or
// embedded); nothing is crawled at runtime.
func EnsurePackages(engine, version, mirror string) (*Index, *VersionInfo, error) {
	ix, err := Ensure(engine, mirror)
	if err != nil {
		return nil, nil, err
	}
	info := ix.Version(version)
	if info == nil {
		return nil, nil, fmt.Errorf("unknown %s version %q (update dbpod or its metadata to pick up newer releases)", engine, version)
	}
	if !info.PackagesFetched {
		return nil, nil, fmt.Errorf("metadata for %s@%s has no package list (regenerate metadata with cmd/metadata-gen or update dbpod)", engine, version)
	}
	return ix, info, nil
}

// EnsureVersions is an alias for call sites that only need the version list;
// it uses the same no-crawl chain as Ensure.
func EnsureVersions(engine, mirror string) (*Index, error) {
	return Ensure(engine, mirror)
}
