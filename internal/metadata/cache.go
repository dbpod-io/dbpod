package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbpod-io/dbpod/internal/project"
)

// cachePath returns the on-disk metadata file for an engine
// (<DBPOD_HOME>/metadata/<engine>.json).
func cachePath(engine string) (string, error) {
	dir, err := project.MetadataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, engine+".json"), nil
}

// Load reads the metadata file for engine, or returns nil when absent.
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

// Save persists the index for engine.
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

// Disabled reports whether the engine's metadata file is disabled
// (renamed to <engine>.json.disabled by `registry disable`).
func Disabled(engine string) bool {
	dir, err := project.MetadataDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, engine+".json.disabled"))
	return err == nil
}

// SetDisabled renames the engine's metadata file to/from the disabled
// form. Disabling an engine whose file does not exist yet seeds it from
// the embedded copy first, so the disabled state is never silently lost.
func SetDisabled(engine string, disabled bool) error {
	path, err := cachePath(engine)
	if err != nil {
		return err
	}
	if disabled {
		if _, err := os.Stat(path); err != nil {
			ix, embErr := Embedded(engine)
			if embErr != nil {
				return embErr
			}
			if err := Save(engine, ix); err != nil {
				return err
			}
		}
		return os.Rename(path, path+".disabled")
	}
	if _, err := os.Stat(path + ".disabled"); err != nil {
		return nil // not disabled
	}
	if err := os.Rename(path+".disabled", path); err != nil {
		return err
	}
	// a renamed-back file may be empty (placeholder): drop it so the next
	// use re-seeds from the embedded copy
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) == 0 {
		_ = os.Remove(path)
	}
	return nil
}

// EnsureBuiltin returns a usable index for a built-in engine. The first
// use copies the metadata embedded in the binary into the configuration
// directory; from then on that file is the single source — `registry
// update` refreshes it explicitly. There is no auto-refresh and no
// network access here. A disabled engine (registry disable) is not usable.
func EnsureBuiltin(engine string) (*Index, error) {
	if Disabled(engine) {
		return nil, fmt.Errorf("engine %s is disabled (dbpod registry enable %s)", engine, engine)
	}
	if ix, err := Load(engine); err != nil {
		return nil, err
	} else if ix != nil {
		return ix, nil
	}
	ix, err := Embedded(engine)
	if err != nil {
		return nil, err
	}
	if ix.BaseURL == "" {
		ix.BaseURL = OfficialDownloadsBase
	}
	if err := Save(engine, ix); err != nil {
		return ix, nil // usable even if caching failed
	}
	return ix, nil
}

// EnsurePackages returns the index plus the version info with its package
// list. All packages come from the metadata file; nothing is crawled at
// runtime.
func EnsurePackages(engine, version string) (*Index, *VersionInfo, error) {
	ix, err := EnsureBuiltin(engine)
	if err != nil {
		return nil, nil, err
	}
	info := ix.Version(version)
	if info == nil {
		return nil, nil, fmt.Errorf("unknown %s version %q (run: dbpod registry update %s)", engine, version, engine)
	}
	if !info.PackagesFetched {
		return nil, nil, fmt.Errorf("metadata for %s@%s has no package list (regenerate metadata with cmd/metadata-gen)", engine, version)
	}
	return ix, info, nil
}
