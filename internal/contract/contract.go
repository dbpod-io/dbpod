// Package contract defines the data shapes with which config-declared
// engines (such as MariaDB) describe themselves to dbpod. An engine is
// declared as a manifest file in $DBPOD_HOME/engines.d:
//
//   - family: the lifecycle machinery to reuse ("mysql")
//   - one command line per lifecycle action (init/start/client/exec/
//     shutdown): the config defines the process; dbpod provides the
//     template variables, validates and executes
//   - config: the server configuration file content (a template rendered
//     with the same variables)
//   - index_url / download / sources: distribution (version index
//     location, package URL templates, mirrors)
//
// Everything declared here is treated as a PLAN: dbpod validates it
// (the command binary must resolve inside the engine's versions dir,
// writes stay in the instance datadir, no shell) before executing.
package contract

import (
	"fmt"
	"regexp"
	"strings"
)

// Version is the profile contract revision this dbpod build speaks.
const Version = 1

// Command template variables the family machinery provides. The same
// context renders the server config template; exec additionally
// provides sql. Values are injected, never re-rendered.
//
//	bind_address  host  port  config_path  datadir  data_root  basedir
//	socket  pid_file  tmpdir  log_dir  binlog_dir  relay_dir
//	innodb_data_dir  innodb_log_dir  server_id   (+ sql for exec)
//
// bind_address is what the server config binds (the instance's `bind`
// setting, default 127.0.0.1); host is what clients connect to — they
// differ only for wildcard binds (0.0.0.0/*), where clients fall back to
// the loopback. All client-facing connections go through TCP so one
// manifest serves every platform.
//
// Command templates are pure variable substitution (no expressions):
// Tokens() splits a command into argv slots — whitespace separates
// arguments except inside {{ }} placeholders, so a placeholder always
// occupies exactly one slot and values containing spaces stay a single
// argument.
const (
	ConfigPath = "config_path"
	Socket     = "socket"
	Port       = "port"
	Host       = "host"
	SQL        = "sql"
)

// EngineProfile is the process definition of a MySQL-family engine as
// pure data: five command-line templates (one per lifecycle action) plus
// the server configuration content.
type EngineProfile struct {
	ContractVersion int    `json:"contract_version" yaml:"-"`
	Engine          string `json:"engine" yaml:"-"`

	// Command templates, one per lifecycle action.
	Init     string `json:"init" yaml:"init"`         // initialize an empty datadir
	Start    string `json:"start" yaml:"start"`       // run the server (detached)
	Client   string `json:"client" yaml:"client"`     // interactive SQL shell
	Exec     string `json:"exec" yaml:"exec"`         // run inline SQL ({{ sql }})
	Shutdown string `json:"shutdown" yaml:"shutdown"` // graceful shutdown

	// Config is the server configuration file content (a template,
	// rendered with the same variables and written into the datadir).
	Config string `json:"config" yaml:"config"`
}

// Validate checks the data-level invariants of a profile. Security
// checks (binary resolution inside the versions dir) happen at
// execution time.
func (p *EngineProfile) Validate() error {
	if p.ContractVersion != Version {
		return fmt.Errorf("contract version %d, want %d", p.ContractVersion, Version)
	}
	if p.Engine == "" {
		return fmt.Errorf("engine name is empty")
	}
	for _, c := range []struct {
		action, command string
	}{{"init", p.Init}, {"start", p.Start}, {"client", p.Client}, {"exec", p.Exec}, {"shutdown", p.Shutdown}} {
		if err := validateCommand(c.action, c.command); err != nil {
			return err
		}
	}
	if p.Config == "" {
		return fmt.Errorf("config is empty")
	}
	return nil
}

// validateCommand enforces the shape of one command template: non-empty,
// first token a bare file name (resolved inside the distribution at
// execution time), no absolute-path literals (paths come from variables,
// which the family pins to the instance/distribution scope).
func validateCommand(action, command string) error {
	tokens, err := Tokens(command)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if len(tokens) == 0 {
		return fmt.Errorf("%s command is empty", action)
	}
	if strings.ContainsAny(tokens[0], "/\\") || strings.Contains(tokens[0], "..") {
		return fmt.Errorf("%s: binary %q must be a bare file name (resolved inside the distribution)", action, tokens[0])
	}
	for _, t := range tokens[1:] {
		if strings.HasPrefix(t, "/") {
			return fmt.Errorf("%s: absolute path %q not allowed; use template variables", action, t)
		}
	}
	return nil
}

// Tokens splits a command template into argv slots. Whitespace separates
// arguments except inside {{ }} placeholders, which always form a single
// slot together with any literal text around them.
func Tokens(command string) ([]string, error) {
	var argv []string
	var cur strings.Builder
	inPlaceholder := false
	flush := func() {
		if cur.Len() > 0 {
			argv = append(argv, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case !inPlaceholder && c == '{' && i+1 < len(command) && command[i+1] == '{':
			inPlaceholder = true
			cur.WriteString("{{")
			i++
		case inPlaceholder && c == '}' && i+1 < len(command) && command[i+1] == '}':
			inPlaceholder = false
			cur.WriteString("}}")
			i++
		case !inPlaceholder && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	if inPlaceholder {
		return nil, fmt.Errorf("unterminated {{ in command %q", command)
	}
	flush()
	return argv, nil
}

// placeholderRe matches a {{ variable }} reference inside a token.
var placeholderRe = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// Substitute replaces the {{ variable }} references of one argv slot with
// their context values. Values are never re-scanned.
func Substitute(token string, ctx map[string]any) (string, error) {
	var missing string
	out := placeholderRe.ReplaceAllStringFunc(token, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		v, ok := ctx[name]
		if !ok {
			missing = name
			return ""
		}
		return fmt.Sprint(v)
	})
	if missing != "" {
		return "", fmt.Errorf("unknown variable %q", missing)
	}
	return out, nil
}
