package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shapled/dbpod/internal/config"
	"github.com/shapled/dbpod/internal/dist"
	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/server"
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

// projectRecord loads the server record of the current project (by yaml name
// or directory name).
func projectRecord() (*server.Record, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, fmt.Errorf("no %s found (run `dbpod init` first)", config.FileName)
	}
	c, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	return serverGet(projectName(c, dir))
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

func serverWasInitialized(r *server.Record) bool {
	_, err := os.Stat(filepath.Join(r.DataDir, initMarker))
	return err == nil
}

func markInitialized(r *server.Record) error {
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

func printStatus(engName string, port int) {
	fmt.Fprintf(os.Stdout, "ready: mysql -h 127.0.0.1 -P %d -u root\n", port)
}
