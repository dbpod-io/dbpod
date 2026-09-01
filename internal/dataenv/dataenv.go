// Package dataenv manages named, project-local database data environments
// (.dbpod/data/<name>/) including physical snapshots.
package dataenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shapled/dbpod/internal/project"
)

// Env describes one data environment.
type Env struct {
	Name      string    `json:"name"`
	Engine    string    `json:"engine"`
	Version   string    `json:"version"` // requested spec, may be a series like "8.0"
	CreatedAt time.Time `json:"created_at"`
}

// dir returns the directory of a named environment.
func dir(name string) (string, error) {
	root, err := project.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// DataPath is the datadir (mysqld datadir) of the environment.
func (e *Env) DataPath() (string, error) {
	d, err := dir(e.Name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "data"), nil
}

// SnapshotPath is the directory holding snapshots of the environment.
func (e *Env) SnapshotPath() (string, error) {
	d, err := dir(e.Name)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "snapshots"), nil
}

// Load reads env.json of a named environment.
func Load(name string) (*Env, error) {
	d, err := dir(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(d, "env.json"))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("data environment %q does not exist", name)
	}
	if err != nil {
		return nil, err
	}
	var e Env
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("corrupt env.json for %q: %w", name, err)
	}
	return &e, nil
}

// Save persists env.json.
func (e *Env) Save() error {
	d, err := dir(e.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "env.json"), data, 0o644)
}

// Create makes a new data environment. Refuses duplicates.
func Create(name, engine, version string) (*Env, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("invalid data environment name %q", name)
	}
	d, err := dir(name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(d, "env.json")); err == nil {
		return nil, fmt.Errorf("data environment %q already exists", name)
	}
	if err := os.MkdirAll(filepath.Join(d, "data"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(d, "snapshots"), 0o755); err != nil {
		return nil, err
	}
	e := &Env{Name: name, Engine: engine, Version: version, CreatedAt: time.Now()}
	if err := e.Save(); err != nil {
		return nil, err
	}
	return e, nil
}

// List returns all environments of the current project, sorted by name.
func List() ([]*Env, error) {
	root, err := project.DataDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Env
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		e, err := Load(ent.Name())
		if err != nil {
			continue // not a data environment
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove deletes a data environment directory tree.
func Remove(name string) error {
	if _, err := Load(name); err != nil {
		return err
	}
	d, err := dir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(d)
}

// Exists reports whether a named data environment exists.
func Exists(name string) bool {
	_, err := Load(name)
	return err == nil
}

// Resolve interprets --data values: a known environment name or a filesystem
// path. Returns the env, its datadir and whether it is a named environment.
func Resolve(spec, engine, version string) (*Env, string, bool, error) {
	if spec == "" {
		return nil, "", false, fmt.Errorf("data environment or path is required")
	}
	if e, err := Load(spec); err == nil {
		dp, err := e.DataPath()
		return e, dp, true, err
	}
	// treat as a raw path (may live outside .dbpod)
	abs, err := filepath.Abs(spec)
	if err != nil {
		return nil, "", false, err
	}
	e := &Env{Name: filepath.Base(abs), Engine: engine, Version: version, CreatedAt: time.Now()}
	return e, abs, false, nil
}

// DirSize returns the recursive size in bytes of path (0 when missing).
func DirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := entry.Info(); err == nil && !entry.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
