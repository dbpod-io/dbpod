// Package server manages database server processes: project-local service
// records, detached background start (no daemon), pid-based liveness and
// graceful shutdown.
package server

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

// Record is the persisted state of one server instance.
type Record struct {
	Name      string    `json:"name"`
	Engine    string    `json:"engine"`
	Version   string    `json:"version"`
	DataDir   string    `json:"data_dir"` // absolute datadir
	DataEnv   string    `json:"data_env"` // data environment name ("" for raw paths)
	Port      int       `json:"port"`
	PID       int       `json:"pid"`
	LogPath   string    `json:"log_path"`
	CreatedAt time.Time `json:"created_at"`
}

// pidFile is the engine-managed pid file inside the datadir.
func pidFile(dataDir string) string { return filepath.Join(dataDir, "dbpod.pid") }

// load reads a service record by name.
func load(name string) (*Record, error) {
	path, err := project.ServiceStatePath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("server %q does not exist", name)
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("corrupt server record %q: %w", name, err)
	}
	return &r, nil
}

// save persists a service record.
func (r *Record) save() error {
	dir, err := project.ServicesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := project.ServiceStatePath(r.Name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// remove deletes the service record (and its stale pid file).
func (r *Record) remove() error {
	path, err := project.ServiceStatePath(r.Name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(r.DataDir, "dbpod.pid"))
	return nil
}

// List returns all service records of the current project, sorted by name.
func List() ([]*Record, error) {
	dir, err := project.ServicesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
			continue
		}
		r, err := load(strings.TrimSuffix(ent.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
