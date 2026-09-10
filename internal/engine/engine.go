// Package engine defines Provider, the one abstraction every database
// engine implements: identity, version resolution, download plans, base
// making (install) and instance lifecycle — the complete surface dbpod
// needs to install, start, use and stop an engine. The builtin providers
// live in internal/providers (mysql, postgres); config-declared engines
// (internal/external) implement the same interface from their manifests.
package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dbpod-io/dbpod/internal/metadata"
)

// Options carries everything a provider needs to run one server instance.
type Options struct {
	BinDir      string // directory containing the engine binaries (dist root + /bin)
	DataDir     string // datadir for this instance
	Port        int
	BindAddress string // address the server binds and clients connect to ("" = 127.0.0.1)
	Socket      string // optional unix socket path ("" = TCP only)
	Name        string // server name, used for pid/log file naming
}

// ConnectHost derives the host clients should connect to from the server
// bind address: wildcard binds listen on every interface, but 0.0.0.0 is
// not reliably connectable, so clients fall back to the loopback.
func ConnectHost(bind string) string {
	switch bind {
	case "", "0.0.0.0", "*":
		return "127.0.0.1"
	default:
		return bind
	}
}

// DownloadPlan is everything needed to fetch an engine distribution.
// Its JSON shape is the helper contract for resolve-download (see
// internal/contract).
type DownloadPlan struct {
	Version string         `json:"version"`
	Main    DownloadFile   `json:"main"`
	Deps    []DownloadFile `json:"deps,omitempty"`
}

// DownloadFile is one archive to download (with checksum when published).
type DownloadFile struct {
	URL          string      `json:"url"`
	FallbackURL  string      `json:"fallback_url,omitempty"`
	SHA256       string      `json:"sha256,omitempty"`
	MD5          string      `json:"md5,omitempty"`
	Size         int64       `json:"size,omitempty"`
	Kind         string      `json:"kind"` // tar.gz | tar.xz | zip | deb | rpm
	RootDir      string      `json:"root_dir,omitempty"`
	ExtractRules [][2]string `json:"extract_rules,omitempty"`
}

// Provider is implemented per database engine — one interface covering
// the whole path from "which versions exist" to "the server is running".
type Provider interface {
	// Name returns the engine identifier ("mysql").
	Name() string

	// --- versions ---

	// EnsureVersions returns the engine's version index, refreshing when
	// stale (builtin engines seed their embedded copy on first use).
	EnsureVersions() (*metadata.Index, error)

	// SeriesOf returns the series a version belongs to (see engine impls;
	// the newest calendar release represents "innovation").
	SeriesOf(version string, lts, isLatest bool) []string

	// ResolveVersion maps a possibly-series version ("8.0") to a full
	// version ("8.0.46"), preferring locally installed matches.
	ResolveVersion(version string) (string, error)

	// ResolveDownload returns the package of version for the platform:
	// main archive plus companion dependency archives.
	ResolveDownload(version, goos, goarch string) (DownloadPlan, error)

	// --- base making ---

	// Install lays the distribution described by plan out into base:
	// download, verify, extract and mark the layout root. Most providers
	// delegate to the shared dist machinery (dist.InstallBase); a
	// provider may run its own pipeline instead.
	Install(plan DownloadPlan, base string, stdout io.Writer) error

	// --- lifecycle ---

	// BinaryNames returns the daemon, client and admin binary names.
	BinaryNames() (server, client, admin string)

	// ExecPaths lists directories (relative to the distribution root) that
	// hold user-facing binaries; dbpod exec prepends them to PATH.
	ExecPaths() []string

	// DataDirInitialized reports whether the datadir has been initialized.
	DataDirInitialized(opts Options) bool

	// InitDataDir initializes an empty datadir (root account without password).
	InitDataDir(opts Options) error

	// ServerArgs builds the daemon command line for a detached start;
	// argv[0] (the server binary) is resolved by the caller.
	ServerArgs(opts Options) []string

	// WaitReady polls until the server accepts connections or timeout.
	WaitReady(opts Options, timeout func() bool) error

	// ShutdownArgs builds the admin command line for a graceful shutdown.
	ShutdownArgs(opts Options) []string

	// ClientArgs builds the interactive client command line, connecting
	// as the bootstrap superuser.
	ClientArgs(opts Options) []string

	// ExecArgs builds the client command line to run inline SQL (-e).
	// SQL files are executed via client stdin.
	ExecArgs(opts Options, inlineSQL string) []string

	// SocketPath returns the unix socket path the server listens on for
	// these options ("" when the platform has no sockets).
	SocketPath(opts Options) string

	// WriteConfig renders the server configuration file into the datadir
	// and returns its path. The config pins every writable location
	// (data, tmp, socket, pid, logs) inside the datadir.
	WriteConfig(opts Options) (string, error)

	// Env returns additional environment variables the engine needs for
	// every process it spawns (server, init and client tools), e.g.
	// LD_LIBRARY_PATH for engines with bundled shared libraries.
	Env(opts Options) []string
}

// registry holds the known providers: builtins register at init, mounted
// config engines at CLI startup.
var registry = map[string]Provider{}

// Register adds a provider implementation to the registry.
func Register(p Provider) {
	registry[p.Name()] = p
}

// Get returns the provider of the named engine.
func Get(name string) (Provider, error) {
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q (registered: %v)", name, Supported())
	}
	return p, nil
}

// Supported lists registered engine names (sorted).
func Supported() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Providers returns all registered providers sorted by engine name.
func Providers() []Provider {
	out := make([]Provider, 0, len(registry))
	for _, name := range Supported() {
		out = append(out, registry[name])
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
