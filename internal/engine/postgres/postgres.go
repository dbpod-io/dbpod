// Package postgres implements the engine.Engine interface for PostgreSQL
// (10+), using the portable EDB binaries on windows/macOS and PGDG-extracted
// binaries on Linux.
package postgres

import (
	"embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/flosch/pongo2/v7"
	"github.com/dbpod-io/dbpod/internal/engine"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for PostgreSQL.
type Engine struct{}

func (e *Engine) Name() string { return "postgres" }

func (e *Engine) BinaryNames() (server, client, admin string) {
	return "postgres", "psql", "pg_ctl"
}

// ExecPaths: PostgreSQL ships its user-facing binaries in bin/.
func (e *Engine) ExecPaths() []string { return []string{"bin"} }

func (e *Engine) DataDirInitialized(opts engine.Options) bool {
	// initdb writes PG_VERSION into the data directory
	_, err := os.Stat(filepath.Join(opts.DataDir, "data", "PG_VERSION"))
	return err == nil
}

// superUser is the bootstrap superuser created by initdb (-U).
const superUser = "postgres"

// instanceRoot maps Options to the instance root: opts.DataDir is the root
// and the cluster lives in <root>/data (kept symmetric with the MySQL
// engine layout so instance bookkeeping stays uniform).
func (e *Engine) dataDir(opts engine.Options) string {
	return filepath.Join(opts.DataDir, "data")
}

func (e *Engine) socketDir(opts engine.Options) string {
	return filepath.Join(opts.DataDir, "tmp")
}

func (e *Engine) SocketPath(opts engine.Options) string {
	return filepath.Join(e.socketDir(opts), fmt.Sprintf(".s.PGSQL.%d", opts.Port))
}

// Env returns the shared-library path injection needed by PGDG-extracted
// binaries on Linux (no-op on other platforms).
func (e *Engine) Env(opts engine.Options) []string {
	root := filepath.Dir(opts.BinDir) // engine distribution root
	return []string{
		"LD_LIBRARY_PATH=" + filepath.Join(root, "shared_libs") + ":" + filepath.Join(root, "lib"),
	}
}

// WriteConfig renders postgresql.conf and pg_hba.conf into the instance
// root. Everything the server writes stays inside the instance root.
func (e *Engine) WriteConfig(opts engine.Options) (string, error) {
	for _, d := range []string{e.dataDir(opts), e.socketDir(opts), filepath.Join(opts.DataDir, "log")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	ctx := pongo2.Context{
		"port":             opts.Port,
		"socket_dir":       e.socketDir(opts),
		"log_dir":          filepath.Join(opts.DataDir, "log"),
		"data_dir":         e.dataDir(opts),
		"listen_addresses": "127.0.0.1",
	}

	confPath := filepath.Join(opts.DataDir, "postgresql.conf")
	if err := renderTemplateFile("postgresql.conf.tmpl", confPath, ctx); err != nil {
		return "", err
	}
	hbaPath := filepath.Join(opts.DataDir, "pg_hba.conf")
	if err := renderTemplateFile("pg_hba.conf.tmpl", hbaPath, ctx); err != nil {
		return "", err
	}
	return confPath, nil
}

// InitDataDir runs initdb to create a new cluster with a trust-auth
// postgres superuser (aligned with the MySQL engine's insecure bootstrap).
func (e *Engine) InitDataDir(opts engine.Options) error {
	initdb, err := e.binary(opts, "initdb")
	if err != nil {
		return err
	}
	cmd := exec.Command(initdb,
		"-D", e.dataDir(opts),
		"-U", superUser,
		"--auth=trust",
		"--no-locale",
		"-E", "UTF8",
	)
	cmd.Env = append(os.Environ(), e.Env(opts)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb failed: %w\n%s", err, out)
	}
	_, err = e.WriteConfig(opts)
	return err
}

// ServerArgs builds the postgres command line: the cluster directory plus
// explicit config file locations (both live in the instance root).
func (e *Engine) ServerArgs(opts engine.Options) []string {
	return []string{
		"-D", e.dataDir(opts),
		"-c", "config_file=" + filepath.Join(opts.DataDir, "postgresql.conf"),
		"-c", "hba_file=" + filepath.Join(opts.DataDir, "pg_hba.conf"),
	}
}

// WaitReady polls the TCP port until the server accepts connections.
func (e *Engine) WaitReady(opts engine.Options, timeout func() bool) error {
	addr := fmt.Sprintf("127.0.0.1:%d", opts.Port)
	deadline := 60 * time.Second
	start := time.Now()
	for time.Since(start) < deadline {
		if timeout != nil && timeout() {
			return fmt.Errorf("timed out waiting for %s to become ready", addr)
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("server did not become ready on %s within %s", addr, deadline)
}

// ShutdownArgs builds the pg_ctl fast-stop command line (-w waits, the
// fast mode disconnects clients instead of waiting for them).
func (e *Engine) ShutdownArgs(opts engine.Options) []string {
	return []string{"stop", "-D", e.dataDir(opts), "-m", "fast", "-w", "-t", "60"}
}

// ClientArgs builds the psql command line: postgres superuser over the
// instance unix socket.
func (e *Engine) ClientArgs(opts engine.Options) []string {
	return []string{
		"-U", superUser,
		"-h", e.socketDir(opts),
		"-p", fmt.Sprint(opts.Port),
	}
}

// ExecArgs builds the psql command line to run inline SQL (-c).
func (e *Engine) ExecArgs(opts engine.Options, inlineSQL string) []string {
	args := e.ClientArgs(opts)
	if inlineSQL != "" {
		args = append(args, "-c", inlineSQL)
	}
	return args
}

// Env re-declared for clarity; see the exported wrapper above.

func (e *Engine) binary(opts engine.Options, name string) (string, error) {
	p := filepath.Join(opts.BinDir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found in %s (engine not installed?)", name, opts.BinDir)
	}
	return p, nil
}

// renderTemplateFile renders an embedded pongo2 template to a file.
func renderTemplateFile(name, dest string, ctx pongo2.Context) error {
	f, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return fmt.Errorf("template %s: %w", name, err)
	}
	tpl, err := pongo2.FromBytes(f)
	if err != nil {
		return fmt.Errorf("template %s: %w", name, err)
	}
	out, err := tpl.Execute(ctx)
	if err != nil {
		return fmt.Errorf("template %s: %w", name, err)
	}
	return os.WriteFile(dest, []byte(out), 0o600)
}
