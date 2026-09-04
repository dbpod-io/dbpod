// Package mysql implements the engine.Engine interface for MySQL 8.x+.
package mysql

import (
	"crypto/sha1"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/flosch/pongo2/v7"
	"github.com/shapled/dbpod/internal/engine"
	"github.com/shapled/dbpod/internal/project"
)

// defaultMySQLConfigTemplate is embedded at build time and seeded into
// DBPOD_HOME/templates on first use.
//
//go:embed templates/mysql.cnf.tmpl
var defaultMySQLConfigTemplate []byte

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for MySQL.
type Engine struct{}

func (e *Engine) Name() string { return "mysql" }

func (e *Engine) BinaryNames() (server, client, admin string) {
	return "mysqld", "mysql", "mysqladmin"
}

// ExecPaths: MySQL ships its user-facing binaries in bin/.
func (e *Engine) ExecPaths() []string {
	return []string{"bin"}
}

// systemMarkers: MySQL 8 creates mysql.ibd (system tablespace) and the
// mysql/ schema directory during --initialize. The data lives under
// <datadir>/data in the dbpod layout.
var systemMarkers = []string{"mysql.ibd", "ib_bufferpool"}

func (e *Engine) DataDirInitialized(opts engine.Options) bool {
	return engine.LooksInitialized(filepath.Join(opts.DataDir, "data"), systemMarkers...)
}

// configPath returns the rendered server configuration file (kept inside
// the datadir so an instance is fully self-contained).
func (e *Engine) configPath(opts engine.Options) string {
	return filepath.Join(opts.DataDir, "my.cnf")
}

// socketPath resolves the unix socket path of an instance: explicit
// option, <datadir>/mysql.sock, or a hashed temp path when the datadir
// path exceeds the sun_path limit.
func (e *Engine) socketPath(opts engine.Options) string {
	if opts.Socket != "" {
		return opts.Socket
	}
	socket := filepath.Join(opts.DataDir, "mysql.sock")
	if len(socket) > 90 { // unix socket sun_path limit (~104) with margin
		sum := sha1.Sum([]byte(socket))
		socket = filepath.Join(os.TempDir(), fmt.Sprintf("dbpod-%x.sock", sum[:6]))
	}
	return socket
}

// SocketPath exposes the unix socket path of an instance.
func (e *Engine) SocketPath(opts engine.Options) string {
	return e.socketPath(opts)
}

// WriteConfig renders the instance configuration file (my.cnf) into the
// datadir. The datadir acts as the instance root: data, innodb files,
// bin/relay logs, temp files, socket, pid and server logs all live inside
// it — nothing leaks to system locations like /tmp.
//
// The config is rendered from a pongo2 template. The embedded default
// template is seeded into DBPOD_HOME/templates on first use, where it can
// be customized; a template found there always wins.
func (e *Engine) WriteConfig(opts engine.Options) (string, error) {
	sock := e.socketPath(opts)
	root := opts.DataDir
	basedir := filepath.Dir(opts.BinDir) // distribution root (bin/ lives inside)

	if err := seedGlobalTemplate(); err != nil {
		return "", err
	}
	tplDir, err := project.TemplatesDir()
	if err != nil {
		return "", err
	}
	tpl, err := pongo2.FromFile(filepath.Join(tplDir, "mysql.cnf.tmpl"))
	if err != nil {
		return "", fmt.Errorf("config template: %w", err)
	}
	out, err := tpl.Execute(pongo2.Context{
		"basedir":         basedir,
		"datadir":         filepath.Join(root, "data"),
		"data_root":       root,
		"socket":          sock,
		"pid_file":        filepath.Join(root, "dbpod.pid"),
		"tmpdir":          filepath.Join(root, "tmp"),
		"log_dir":         filepath.Join(root, "log"),
		"binlog_dir":      filepath.Join(root, "bin-logs"),
		"relay_dir":       filepath.Join(root, "relay-logs"),
		"innodb_data_dir": filepath.Join(root, "innodb", "data"),
		"innodb_log_dir":  filepath.Join(root, "innodb", "logs"),
		"port":            opts.Port,
		"server_id":       opts.Port, // unique per instance
		"bind_address":    "127.0.0.1",
	})
	if err != nil {
		return "", fmt.Errorf("config template: %w", err)
	}

	// multi-level directories must exist before mysqld starts
	dirs := []string{
		filepath.Join(root, "data"),
		filepath.Join(root, "log"),
		filepath.Join(root, "tmp"),
		filepath.Join(root, "bin-logs"),
		filepath.Join(root, "relay-logs"),
		filepath.Join(root, "innodb", "data"),
		filepath.Join(root, "innodb", "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}

	path := e.configPath(opts)
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// seedGlobalTemplate materializes the embedded default template into
// DBPOD_HOME/templates (once), where users can customize it.
func seedGlobalTemplate() error {
	tplDir, err := project.TemplatesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(tplDir, "mysql.cnf.tmpl")
	if _, err := os.Stat(dst); err == nil {
		return nil // user template (or already seeded) — never overwrite
	}
	return os.WriteFile(dst, defaultMySQLConfigTemplate, 0o644)
}

// InitDataDir runs mysqld --initialize-insecure with the instance config
// (root account with empty password).
func (e *Engine) InitDataDir(opts engine.Options) error {
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return err
	}
	mysqld, err := engineBinary(opts, "mysqld")
	if err != nil {
		return err
	}
	cfg, err := e.WriteConfig(opts)
	if err != nil {
		return err
	}
	args := []string{"--defaults-file=" + cfg, "--initialize-insecure"}
	if out, err := runCommand(mysqld, args); err != nil {
		log := filepath.Join(opts.DataDir, "log", "error.log")
		return fmt.Errorf("mysqld --initialize-insecure failed: %w\n--- %s ---\n%s", err, log, out)
	}
	return nil
}

// ServerArgs builds the mysqld command line. Everything is configured via
// the generated my.cnf; --defaults-file must be the first argument.
func (e *Engine) ServerArgs(opts engine.Options) []string {
	return []string{"--defaults-file=" + e.configPath(opts)}
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

// ShutdownArgs builds the mysqladmin shutdown command line (TCP).
func (e *Engine) ShutdownArgs(opts engine.Options) []string {
	return []string{"-h", "127.0.0.1", "-P", fmt.Sprint(opts.Port), "-u", "root", "shutdown"}
}

// ClientArgs builds the interactive client command line: root over the
// instance unix socket (no TCP needed on the local machine).
func (e *Engine) ClientArgs(opts engine.Options) []string {
	return []string{"-u", "root", "-S", e.socketPath(opts)}
}

// ExecArgs builds the client command line to run inline SQL (-e).
func (e *Engine) ExecArgs(opts engine.Options, inlineSQL string) []string {
	args := e.ClientArgs(opts)
	if inlineSQL != "" {
		args = append(args, "-e", inlineSQL)
	}
	return args
}

// Env: MySQL distributions are self-contained; no extra variables needed.
func (e *Engine) Env(opts engine.Options) []string { return nil }

func engineBinary(opts engine.Options, name string) (string, error) {
	p := filepath.Join(opts.BinDir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found in %s (engine not installed?)", name, opts.BinDir)
	}
	return p, nil
}

// runCommand executes a synchronous engine binary, returning combined output.
func runCommand(bin string, args []string) (string, error) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
