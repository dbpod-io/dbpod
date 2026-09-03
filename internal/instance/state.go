// Package instance manages database instance processes: detached background
// start (no daemon), pid-based liveness and graceful shutdown.
package instance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shapled/dbpod/internal/project"
)

// Record is the persisted state of one instance.
type Record struct {
	Name         string    `json:"name"`
	Engine       string    `json:"engine"`
	Version      string    `json:"version"`
	DataDir      string    `json:"data_dir"` // absolute datadir
	DataEnv      string    `json:"data_env"` // data environment name ("" for raw paths)
	Port         int       `json:"port"`
	PID          int       `json:"pid"`            // server process (0 = stopped)
	MonitorPID   int       `json:"monitor_pid"`    // per-instance monitor process (0 = none)
	LastExitCode int       `json:"last_exit_code"` // exit code of the last server run
	AutoRemove   bool      `json:"auto_remove"`    // --rm: clean up when the server stops
	LogPath      string    `json:"log_path"`
	CreatedAt    time.Time `json:"created_at"`
}

// pidFile is the engine-managed pid file inside the datadir.
func pidFile(dataDir string) string { return filepath.Join(dataDir, "dbpod.pid") }

// recordPath returns the global record file of a named instance
// (<instances-dir>/<name>.json).
func recordPath(name string) (string, error) {
	dir, err := project.InstancesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// load reads an instance record by name (global, not project-scoped).
func load(name string) (*Record, error) {
	path, err := recordPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("instance %q does not exist: %w", name, err)
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("corrupt instance record %q: %w", name, err)
	}
	return &r, nil
}

// save persists the instance record (global).
func (r *Record) save() error {
	path, err := recordPath(r.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// remove deletes the instance record, its stale pid file, the datadir
// descriptor AND the datadir itself. When the datadir follows the default
// layout (<instances-dir>/<name>/data), the emptied <name> wrapper is
// removed as well. Custom datadir locations are untouched beyond their
// own directory.
func (r *Record) remove() error {
	path, err := recordPath(r.Name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(r.DataDir, "dbpod.pid"))
	_ = os.Remove(filepath.Join(r.DataDir, descriptorName))
	if err := os.RemoveAll(r.DataDir); err != nil {
		return err
	}
	// drop the now-empty default wrapper dir (<instances-dir>/<name>);
	// never touch the parent of a custom datadir location
	if instancesDir, ierr := project.InstancesDir(); ierr == nil {
		if parent := filepath.Dir(r.DataDir); parent != instancesDir &&
			strings.HasPrefix(parent, instancesDir+string(filepath.Separator)) {
			_ = os.Remove(parent) // only succeeds when empty
		}
	}
	return nil
}

// descriptorName is the self-describing copy of the record kept inside the
// datadir, making instances discoverable from the global instances
// directory regardless of where their record lives.
const descriptorName = "dbpod-instance.json"

// writeDescriptor persists the record into the datadir.
func (r *Record) writeDescriptor() error {
	if r.DataDir == "" {
		return nil
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.DataDir, descriptorName), data, 0o644)
}

// ListGlobal scans the instances directory (one level) for datadir
// descriptors, sorted by name.
func ListGlobal(base string) ([]*Record, error) {
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, ent.Name(), "data", descriptorName))
		if err != nil {
			continue // not a dbpod instance
		}
		var r Record
		if json.Unmarshal(data, &r) == nil && r.Name != "" {
			out = append(out, &r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// List returns all instance records (global), sorted by name.
func List() ([]*Record, error) {
	dir, err := project.InstancesDir()
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
