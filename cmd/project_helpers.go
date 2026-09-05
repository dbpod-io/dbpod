package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dbpod-io/dbpod/internal/config"
	"github.com/dbpod-io/dbpod/internal/dist"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/instance"
)

func engineGet(name string) (engine.Engine, error) {
	return engine.Get(name)
}

func mkOpts(dataDir string, port int, binDir string) engine.Options {
	return engine.Options{DataDir: dataDir, Port: port, BinDir: binDir}
}

// projectName derives the project service name: yaml name or dir base.
func projectName(c *config.Config, dir string) string {
	if c.Name != "" {
		return c.Name
	}
	return filepath.Base(dir)
}

func parseEngineSpec(s string) (*dist.PackageRef, error) {
	ref, err := dist.ParseRef(s)
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func engineRef(engine, version string) *dist.PackageRef {
	return &dist.PackageRef{Engine: engine, Version: version}
}

// projectRecord loads the instance record of the current project (by yaml name
// or directory name).
func projectRecord() (*instance.Record, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("no %s found (run `dbpod project init` first)", config.FileName)
	}
	c, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	return instanceGet(projectName(c, dir))
}

// projectInstances returns the instance records related to this project's
// config: those created by `project up` (matching name or data environment).
func projectInstances() ([]*instance.Record, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("no %s found (run `dbpod project init` first)", config.FileName)
	}
	c, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	name := projectName(c, dir)
	records, err := instance.List()
	if err != nil {
		return nil, err
	}
	var out []*instance.Record
	for _, r := range records {
		if r.Name == name || r.DataEnv == name {
			out = append(out, r)
		}
	}
	return out, nil
}

// defaultProjectInstance resolves the instance for project logs/exec:
// an explicit name wins; otherwise the sole project instance is used and
// ambiguity is an error.
func defaultProjectInstance(args []string) (*instance.Record, error) {
	records, err := projectInstances()
	if err != nil {
		return nil, err
	}
	if len(args) > 0 {
		for _, r := range records {
			if r.Name == args[0] {
				return r, nil
			}
		}
	}
	switch len(records) {
	case 1:
		return records[0], nil
	case 0:
		return nil, fmt.Errorf("no instances for this project; run `dbpod project up` first")
	default:
		names := make([]string, len(records))
		for i, r := range records {
			names[i] = r.Name
		}
		return nil, fmt.Errorf("multiple project instances (%s); specify one explicitly", strings.Join(names, ", "))
	}
}

// dbpodDir returns the project-local .dbpod directory (cwd based).
func dbpodDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".dbpod"), nil
}

// ensureGitignore appends .dbpod/ to .gitignore when missing.
func ensureGitignore(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	data, _ := os.ReadFile(path)
	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".dbpod/" {
			return false, nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if content != "" && !strings.HasSuffix(content, "\n") {
		fmt.Fprintln(f)
	}
	_, err = fmt.Fprintln(f, ".dbpod/")
	return err == nil, err
}

// initMarker is written into the datadir after init-sql import.
const initMarker = ".dbpod-init-done"

func instanceWasInitialized(r *instance.Record) bool {
	_, err := os.Stat(filepath.Join(r.DataDir, initMarker))
	return err == nil
}

func instanceMarkInitialized(r *instance.Record) error {
	return os.WriteFile(filepath.Join(r.DataDir, initMarker), []byte("ok\n"), 0o644)
}

// importSQL runs SQL files through the engine client.
func importSQL(engName, version string, files []string, port int) error {
	eng, err := engineGet(engName)
	if err != nil {
		return err
	}
	_, client, _ := eng.BinaryNames() // (server, client, admin)
	clientPath, err := dist.BinaryPath(engName, version, client)
	if err != nil {
		return err
	}
	args := eng.ExecArgs(mkOpts("", port, filepath.Dir(clientPath)), "")
	for _, f := range files {
		fmt.Fprintf(os.Stdout, "importing %s\n", f)
		if err := importViaClient(clientPath, args, f); err != nil {
			return fmt.Errorf("import %s failed: %w", f, err)
		}
	}
	return nil
}

func printStatus() {
	fmt.Fprintf(os.Stdout, "connect: dbpod project exec\n")
}
