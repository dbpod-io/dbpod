// Package project resolves the on-disk layout of dbpod state.
//
// Global state (engine binaries, metadata cache) lives under DBPOD_HOME
// (default ~/.dbpod). Per-project state (volumes, services, logs) lives
// under ./.dbpod relative to the current working directory.
package project

import (
	"os"
	"path/filepath"
)

// DBPodDir returns the project-local state directory (.dbpod in cwd).
func DBPodDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".dbpod"), nil
}

// HomeDir returns the global dbpod home (DBPOD_HOME or ~/.dbpod).
func HomeDir() (string, error) {
	if h := os.Getenv("DBPOD_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".dbpod"), nil
}

// VersionsDir returns the global cache of engine binaries.
func VersionsDir() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "versions"), nil
}

// VersionsDirMust is VersionsDir without error handling (empty string on
// failure) — used where the caller only needs a best-effort comparison.
func VersionsDirMust() string {
	dir, _ := VersionsDir()
	return dir
}

// InstancesDir returns the global directory holding instance datadirs
// (DBPOD_INSTANCES_DIR overrides the default <DBPOD_HOME>/instances).
func InstancesDir() (string, error) {
	if d := os.Getenv("DBPOD_INSTANCES_DIR"); d != "" {
		return d, nil
	}
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "instances"), nil
}

// MetadataDir returns the global directory for downloaded metadata caches.
func MetadataDir() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "metadata"), nil
}

// TemplatesDir returns the global directory for user-customizable config
// templates (seeded from the embedded defaults on first use).
func TemplatesDir() (string, error) {
	h, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "templates"), nil
}

// ImageDir returns the extracted engine directory for engine@version.
func ImageDir(engine, version string) (string, error) {
	v, err := VersionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(v, engine, version, platformDir()), nil
}

// DataDir returns the project-local data environments directory.
func DataDir() (string, error) {
	d, err := DBPodDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "data"), nil
}

// LogsDir returns the project-local logs directory.
func LogsDir() (string, error) {
	d, err := DBPodDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "logs"), nil
}

// ServiceLogPath returns the log file path of a named service.
func ServiceLogPath(name string) (string, error) {
	l, err := LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(l, name+".log"), nil
}

// EnsureDir creates dir (and parents) if missing.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
