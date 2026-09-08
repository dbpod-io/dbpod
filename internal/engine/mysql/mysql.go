// Package mysql implements the engine.Engine interface for the MySQL
// family (MySQL, and external engines such as MariaDB via a
// contract.EngineProfile — see internal/external). The profile defines
// each lifecycle action as a command-line template; this package owns
// the family invariants (layout, socket, config rendering, execution
// variables) and runs the commands.
package mysql

import (
	"crypto/sha1"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flosch/pongo2/v7"
	"github.com/dbpod-io/dbpod/internal/contract"
	"github.com/dbpod-io/dbpod/internal/engine"
	"github.com/dbpod-io/dbpod/internal/project"
)

//go:embed templates/mysql.cnf.tmpl
var defaultConfigTemplate string

// Family invariants: every MySQL-family distribution shares these; they
// are mechanism, not configuration.
const (
	execPaths  = "bin"       // distribution-relative directory of user-facing binaries
	configFile = "my.cnf"    // server configuration file inside the datadir
	socketName = "mysql.sock" // unix socket file name inside the datadir
)

// dataMarkers identify an initialized data directory: the union over the
// family (MySQL 8 system tablespace, MariaDB InnoDB/Aria). Any one match
// counts as initialized, so the union covers every family member.
var dataMarkers = []string{"mysql.ibd", "ib_bufferpool", "ibdata1", "aria_log_control"}

func init() {
	engine.Register(New(DefaultProfile()))
}

// DefaultProfile is the built-in MySQL profile. All connections go
// through TCP: the host variable follows the instance bind address, so
// one manifest serves every platform (Windows has no unix sockets).
func DefaultProfile() contract.EngineProfile {
	p := contract.EngineProfile{
		ContractVersion: contract.Version,
		Engine:          "mysql",
		Init:            "mysqld --defaults-file={{ config_path }} --initialize-insecure",
		Start:           "mysqld --defaults-file={{ config_path }}",
		Client:          "mysql -u root -h {{ host }} -P {{ port }}",
		Exec:            "mysql -u root -h {{ host }} -P {{ port }} -e {{ sql }}",
		Shutdown:        "mysqladmin -u root -h {{ host }} -P {{ port }} shutdown",
	}
	p.Config = defaultConfigTemplate
	return p
}

// Engine implements engine.Engine for a MySQL-family profile. The zero
// value is the built-in MySQL engine.
type Engine struct {
	profile *contract.EngineProfile
}

// New builds a family engine from a profile (validated by the caller).
func New(p contract.EngineProfile) *Engine {
	return &Engine{profile: &p}
}

func (e *Engine) prof() *contract.EngineProfile {
	if e.profile == nil {
		p := DefaultProfile()
		return &p
	}
	return e.profile
}

func (e *Engine) Name() string { return e.prof().Engine }

// BinaryNames returns the server, client and admin binaries — the first
// token of the start, client and shutdown commands respectively.
func (e *Engine) BinaryNames() (server, client, admin string) {
	p := e.prof()
	return firstToken(p.Start), firstToken(p.Client), firstToken(p.Shutdown)
}

// ExecPaths: the family ships its user-facing binaries in bin/.
func (e *Engine) ExecPaths() []string { return []string{execPaths} }

func (e *Engine) DataDirInitialized(opts engine.Options) bool {
	return engine.LooksInitialized(filepath.Join(opts.DataDir, "data"), dataMarkers...)
}

// configPath returns the rendered server configuration file (kept inside
// the datadir so an instance is fully self-contained).
func (e *Engine) configPath(opts engine.Options) string {
	return filepath.Join(opts.DataDir, configFile)
}

// socketPath resolves the unix socket path of an instance: explicit
// option, <datadir>/mysql.sock, or a hashed temp path when the datadir
// path exceeds the sun_path limit.
func (e *Engine) socketPath(opts engine.Options) string {
	if opts.Socket != "" {
		return opts.Socket
	}
	socket := filepath.Join(opts.DataDir, socketName)
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

// templateContext is the variable set the family provides to command and
// config templates. Values are injected, never re-rendered. host is the
// client-side connect target derived from the bind address (wildcard
// binds fall back to the loopback).
func (e *Engine) templateContext(opts engine.Options) pongo2.Context {
	root := opts.DataDir
	sock := e.socketPath(opts)
	bind := opts.BindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}
	return pongo2.Context{
		"basedir":         filepath.Dir(opts.BinDir), // distribution root (bin/ lives inside)
		"datadir":         filepath.Join(root, "data"),
		"data_root":       root,
		"config_path":     e.configPath(opts),
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
		"bind_address":    bind,
		"host":            engine.ConnectHost(bind),
	}
}

// renderCommand turns a command template into argv: tokenize (a
// placeholder always occupies one slot), then substitute the context
// variables into each slot. Pure substitution — values are never
// re-scanned, so SQL containing template syntax passes through verbatim.
func renderCommand(tmpl string, ctx pongo2.Context) ([]string, error) {
	tokens, err := contract.Tokens(tmpl)
	if err != nil {
		return nil, err
	}
	argv := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out, err := contract.Substitute(t, ctx)
		if err != nil {
			return nil, fmt.Errorf("command %q: %w", tmpl, err)
		}
		argv = append(argv, out)
	}
	return argv, nil
}

func firstToken(command string) string {
	t := strings.Fields(command)
	if len(t) == 0 {
		return ""
	}
	return t[0]
}

// WriteConfig renders the instance configuration file into the datadir.
// The datadir acts as the instance root: data, innodb files, bin/relay
// logs, temp files, socket, pid and server logs all live inside it —
// nothing leaks to system locations like /tmp.
//
// The rendered content is the profile's default config; a file at
// DBPOD_HOME/templates/<engine>.cnf.tmpl is a deliberate customization
// and wins. The default itself is never materialized as a live template,
// so profile updates always reach installations without customizations.
// As a reference for customization, the current default is mirrored to
// <engine>.cnf.tmpl.example (kept in sync on every render; renaming it
// to <engine>.cnf.tmpl activates it). Users who write a template take
// over the process — paths must use the template variables (the defaults
// keep every path inside the datadir).
func (e *Engine) WriteConfig(opts engine.Options) (string, error) {
	p := e.prof()
	root := opts.DataDir

	var tpl *pongo2.Template
	tplDir, err := project.TemplatesDir()
	if err != nil {
		return "", err
	}
	syncExampleTemplate(tplDir, p)
	userPath := filepath.Join(tplDir, p.Engine+".cnf.tmpl")
	if _, err := os.Stat(userPath); err == nil {
		t, err := pongo2.FromFile(userPath)
		if err != nil {
			return "", fmt.Errorf("config template: %w", err)
		}
		tpl = t
	} else {
		t, err := pongo2.FromString(p.Config)
		if err != nil {
			return "", fmt.Errorf("config template: %w", err)
		}
		tpl = t
	}
	out, err := tpl.Execute(e.templateContext(opts))
	if err != nil {
		return "", fmt.Errorf("config template: %w", err)
	}

	// multi-level directories must exist before the server starts
	for _, d := range []string{
		filepath.Join(root, "data"),
		filepath.Join(root, "log"),
		filepath.Join(root, "tmp"),
		filepath.Join(root, "bin-logs"),
		filepath.Join(root, "relay-logs"),
		filepath.Join(root, "innodb", "data"),
		filepath.Join(root, "innodb", "logs"),
	} {
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

// syncExampleTemplate mirrors the profile's current default config into
// templates/<engine>.cnf.tmpl.example — a reference for customization.
// The example is never rendered and always reflects the current default,
// so it is simply overwritten on every call.
func syncExampleTemplate(tplDir string, p *contract.EngineProfile) {
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		return // best effort: a missing reference must not break startup
	}
	_ = os.WriteFile(filepath.Join(tplDir, p.Engine+".cnf.tmpl.example"), []byte(p.Config), 0o644)
}

// runCommand executes a family command template synchronously, returning
// combined output.
func (e *Engine) runCommand(opts engine.Options, tmpl string, extra map[string]any) error {
	ctx := e.templateContext(opts)
	for k, v := range extra {
		ctx[k] = v
	}
	argv, err := renderCommand(tmpl, ctx)
	if err != nil {
		return err
	}
	bin, err := engineBinary(opts, argv[0])
	if err != nil {
		return err
	}
	if out, err := exec.Command(bin, argv[1:]...).CombinedOutput(); err != nil {
		log := filepath.Join(opts.DataDir, "log", "error.log")
		return fmt.Errorf("%s failed: %w\n--- %s ---\n%s", argv[0], err, log, out)
	}
	return nil
}

// InitDataDir runs the init command (root account with empty password).
func (e *Engine) InitDataDir(opts engine.Options) error {
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return err
	}
	if _, err := e.WriteConfig(opts); err != nil {
		return err
	}
	return e.runCommand(opts, e.prof().Init, nil)
}

// ServerArgs builds the server command line for the detached start.
func (e *Engine) ServerArgs(opts engine.Options) []string {
	argv, err := renderCommand(e.prof().Start, e.templateContext(opts))
	if err != nil {
		return []string{e.prof().Start} // unreachable for validated profiles
	}
	return argv[1:] // the monitor resolves and executes argv[0]
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

// ShutdownArgs builds the graceful shutdown command line.
func (e *Engine) ShutdownArgs(opts engine.Options) []string {
	argv, err := renderCommand(e.prof().Shutdown, e.templateContext(opts))
	if err != nil {
		return nil
	}
	return argv[1:]
}

// ClientArgs builds the interactive client command line.
func (e *Engine) ClientArgs(opts engine.Options) []string {
	argv, err := renderCommand(e.prof().Client, e.templateContext(opts))
	if err != nil {
		return nil
	}
	return argv[1:]
}

// ExecArgs builds the client command line to run inline SQL.
func (e *Engine) ExecArgs(opts engine.Options, inlineSQL string) []string {
	ctx := e.templateContext(opts)
	ctx[contract.SQL] = inlineSQL // injected as a value, never re-rendered
	argv, err := renderCommand(e.prof().Exec, ctx)
	if err != nil {
		return nil
	}
	return argv[1:]
}

// Env: family distributions are self-contained.
func (e *Engine) Env(opts engine.Options) []string { return nil }

func engineBinary(opts engine.Options, name string) (string, error) {
	p := filepath.Join(opts.BinDir, name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found in %s (engine not installed?)", name, opts.BinDir)
	}
	return p, nil
}
