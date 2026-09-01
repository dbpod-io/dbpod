// Package mysql implements the engine.Engine interface for MySQL 8.x+.
package mysql

import (
	"crypto/sha1"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shapled/dbpod/internal/engine"
)

func init() {
	engine.Register(&Engine{})
}

// Engine implements engine.Engine for MySQL.
type Engine struct{}

func (e *Engine) Name() string { return "mysql" }

func (e *Engine) BinaryNames() (server, client, admin string) {
	return "mysqld", "mysql", "mysqladmin"
}

// systemMarkers: MySQL 8 creates mysql.ibd (system tablespace) and the
// mysql/ schema directory during --initialize.
var systemMarkers = []string{"mysql.ibd", "ib_bufferpool"}

func (e *Engine) DataDirInitialized(opts engine.Options) bool {
	return engine.LooksInitialized(opts.DataDir, systemMarkers...)
}

// InitDataDir runs mysqld --initialize-insecure on an empty datadir
// (root account with empty password).
func (e *Engine) InitDataDir(opts engine.Options) error {
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return err
	}
	mysqld, err := engineBinary(opts, "mysqld")
	if err != nil {
		return err
	}
	args := []string{
		"--no-defaults",
		"--initialize-insecure",
		"--datadir=" + opts.DataDir,
		"--log-error=" + filepath.Join(opts.DataDir, "init-error.log"),
	}
	if out, err := runCommand(mysqld, args); err != nil {
		log := filepath.Join(opts.DataDir, "init-error.log")
		return fmt.Errorf("mysqld --initialize-insecure failed: %w\n--- %s ---\n%s", err, log, out)
	}
	return nil
}

// ServerArgs builds the mysqld command line. mysqld itself daemonizes? No:
// it stays in foreground; dbpod detaches the process.
func (e *Engine) ServerArgs(opts engine.Options) []string {
	args := []string{
		"--no-defaults",
		"--datadir=" + opts.DataDir,
		"--port=" + fmt.Sprint(opts.Port),
		"--bind-address=127.0.0.1",
		"--mysqlx=OFF",
		"--pid-file=" + filepath.Join(opts.DataDir, "dbpod.pid"),
	}
	if opts.Socket != "" {
		args = append(args, "--socket="+opts.Socket)
		return args
	}
	// every instance needs its own socket: the mysqld default
	// (/tmp/mysql.sock) would collide across instances
	socket := filepath.Join(opts.DataDir, "mysql.sock")
	if len(socket) > 90 { // unix socket sun_path limit (~104) with margin
		sum := sha1.Sum([]byte(socket))
		socket = filepath.Join(os.TempDir(), fmt.Sprintf("dbpod-%x.sock", sum[:6]))
	}
	args = append(args, "--socket="+socket)
	return args
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

// ClientArgs builds the interactive client command line.
func (e *Engine) ClientArgs(opts engine.Options) []string {
	return []string{"-h", "127.0.0.1", "-P", fmt.Sprint(opts.Port), "-u", "root"}
}

// ExecArgs builds the client command line to run inline SQL (-e).
func (e *Engine) ExecArgs(opts engine.Options, inlineSQL string) []string {
	args := []string{"-h", "127.0.0.1", "-P", fmt.Sprint(opts.Port), "-u", "root"}
	if inlineSQL != "" {
		args = append(args, "-e", inlineSQL)
	}
	return args
}

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
