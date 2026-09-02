// Package engine abstracts database engines (MySQL today, MariaDB and
// PostgreSQL later): data directory initialization, server lifecycle and
// client access.
package engine

import (
	"fmt"
	"os"
	"path/filepath"
)

// Options carries everything an engine needs to run one server instance.
type Options struct {
	BinDir  string // directory containing the engine binaries (dist root + /bin)
	DataDir string // datadir for this instance
	Port    int
	Socket  string // optional unix socket path ("" = TCP only)
	Name    string // server name, used for pid/log file naming
}

// Engine is implemented per database engine.
type Engine interface {
	// Name returns the engine identifier ("mysql").
	Name() string

	// BinaryNames returns the daemon, client and admin binary names.
	BinaryNames() (server, client, admin string)

	// ExecPaths lists directories (relative to the distribution root) that
	// hold user-facing binaries; dbpod exec prepends them to PATH.
	ExecPaths() []string

	// InitDataDir initializes an empty datadir (root account without password).
	InitDataDir(opts Options) error

	// DataDirInitialized reports whether the datadir has been initialized.
	DataDirInitialized(opts Options) bool

	// ServerArgs builds the daemon command line for a detached start.
	ServerArgs(opts Options) []string

	// WaitReady polls until the server accepts connections or timeout.
	WaitReady(opts Options, timeout func() bool) error

	// ShutdownArgs builds the admin command line for a graceful shutdown.
	ShutdownArgs(opts Options) []string

	// ClientArgs builds the client command line for interactive shell,
	// connecting as root via the instance's unix socket (the most
	// permissive local path).
	ClientArgs(opts Options) []string

	// SocketPath returns the unix socket path the server listens on for
	// these options ("" when the platform has no sockets).
	SocketPath(opts Options) string

	// WriteConfig renders the server configuration file into the datadir
	// and returns its path. The config pins every writable location
	// (data, tmp, socket, pid, logs) inside the datadir.
	WriteConfig(opts Options) (string, error)

	// ExecArgs builds the client command line to run inline SQL (-e).
	// SQL files are executed via client stdin.
	ExecArgs(opts Options, inlineSQL string) []string
}

// registry holds the known engines.
var registry = map[string]Engine{}

// Register adds an engine implementation to the registry (called by
// engine implementations in their init functions).
func Register(e Engine) {
	registry[e.Name()] = e
}

// Get returns the engine implementation by name.
func Get(name string) (Engine, error) {
	e, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q (supported: mysql)", name)
	}
	return e, nil
}

// Supported lists registered engine names.
func Supported() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	return out
}

// LooksInitialized is a generic datadir probe: a datadir is considered
// initialized when it contains a system tablespace or system schema.
func LooksInitialized(dataDir string, systemMarkers ...string) bool {
	for _, m := range systemMarkers {
		if _, err := os.Stat(filepath.Join(dataDir, m)); err == nil {
			return true
		}
	}
	return false
}
